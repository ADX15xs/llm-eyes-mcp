package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/xiaos/llm-eyes-mcp/internal/cache"
	"github.com/xiaos/llm-eyes-mcp/internal/geometry"
	"github.com/xiaos/llm-eyes-mcp/internal/imgproc"
	"github.com/xiaos/llm-eyes-mcp/internal/vlm"
)

type regionDetection struct {
	ID         string
	Box        geometry.Box // normalised [0,1]
	Text       string
	Confidence float64
	Found      bool
}

type compareDetectMeta struct {
	tier   string
	model  string
	rounds int
}

// compareBatchSize bounds how many regions ride in one VLM call. The uoma
// gateway ignores every thinking-disable parameter on vision requests, so a
// multi-region locate prompt burns its whole token budget on reasoning and
// returns NULL content (probes archived under docs/archive; see docs/TODO.md).
// One region per call is the only size verified reliable against that upstream
// — revisit when the gateway honours thinking controls.
const compareBatchSize = 1

// chunkRegions splits rs into consecutive slices of at most size elements.
func chunkRegions(rs []compareRegion, size int) [][]compareRegion {
	if size <= 0 || len(rs) == 0 {
		return [][]compareRegion{rs}
	}
	out := make([][]compareRegion, 0, (len(rs)+size-1)/size)
	for start := 0; start < len(rs); start += size {
		end := start + size
		if end > len(rs) {
			end = len(rs)
		}
		out = append(out, rs[start:end])
	}
	return out
}

func (d *Deps) locateRegions(
	ctx context.Context,
	imgID string,
	proc *imgproc.Output,
	provider vlm.Provider,
	spec *compareSpec,
	specHash string,
	refresh bool,
) ([]regionDetection, compareDetectMeta, error) {
	batches := chunkRegions(spec.Regions, compareBatchSize)
	agg := compareDetectMeta{tier: "L2", model: provider.ModelVersion()}
	out := make([]regionDetection, 0, len(spec.Regions))

	for i, batch := range batches {
		batchSpec := &compareSpec{Version: spec.Version, Regions: batch}
		dets, meta, err := d.locateRegionBatch(ctx, imgID, proc, provider, batchSpec, specHash, i, refresh)
		if err != nil {
			return nil, agg, err
		}
		if meta.model != "" {
			agg.model = meta.model
		}
		agg.rounds += meta.rounds
		out = append(out, dets...)
	}
	if len(batches) > 1 {
		d.logf("compare located %d regions across %d batches (<=%d each)", len(out), len(batches), compareBatchSize)
	}
	return out, agg, nil
}

// forceSingleRegionID makes the spec's id authoritative when a batch holds
// exactly one region: whatever box the model returned is by construction the
// answer to "locate this region", so a wrong or missing id echo must not turn
// into a silent "missing" verdict downstream.
func forceSingleRegionID(spec *compareSpec, dets []regionDetection) {
	if len(spec.Regions) == 1 && len(dets) == 1 {
		dets[0].ID = spec.Regions[0].ID
		dets[0].Box.Label = spec.Regions[0].ID
	}
}

// locateRegionBatch locates one slice of regions, with its own L2 entry and
// bounded re-asks, so a hard batch cannot poison its neighbours.
func (d *Deps) locateRegionBatch(
	ctx context.Context,
	imgID string,
	proc *imgproc.Output,
	provider vlm.Provider,
	spec *compareSpec,
	specHash string,
	batch int,
	refresh bool,
) ([]regionDetection, compareDetectMeta, error) {
	meta := compareDetectMeta{tier: "L2", model: provider.ModelVersion()}

	l2Key := cache.L2Key(imgID, "compare_design_locate", provider.ModelVersion(),
		cache.ParamHash(map[string]string{
			"spec_hash": specHash,
			"batch":     strconv.Itoa(batch),
			"batch_n":   strconv.Itoa(len(spec.Regions)),
		}))
	if d.Cache != nil && !refresh {
		if blob, ok := d.Cache.L2.Get(l2Key); ok {
			var entry measureCacheEntry
			if json.Unmarshal(blob, &entry) == nil && entry.Text != "" && entry.W > 0 && entry.H > 0 {
				if dets, err := parseRegionReply(entry.Text, entry.W, entry.H); err == nil && len(dets) == len(spec.Regions) {
					forceSingleRegionID(spec, dets)
					return dets, meta, nil
				}
			}
			d.Cache.L2.Delete(l2Key)
		}
	}

	maxRounds := d.maxRounds()
	var lastReply string
	var lastErr error

	for round := 1; round <= maxRounds; round++ {
		meta.rounds = round
		prompt := compareLocatePrompt(spec)
		if round > 1 {
			prompt = compareLocateRetryPrompt(spec, lastMissingIDs(spec, lastReply, proc.Width, proc.Height), lastReply)
		}
		resp, err := provider.Complete(ctx, vlm.Request{
			System:    compareSystemPrompt,
			Prompt:    prompt,
			Image:     proc.Data,
			ImageMIME: proc.MIMEType,
			JSONOnly:  true,
		})
		if err != nil {
			lastErr = err
			d.logf("compare batch %d round %d/%d failed: %v", batch, round, maxRounds, err)
			continue
		}
		meta.model = resp.Model
		lastReply = resp.Text

		dets, err := parseRegionReply(resp.Text, proc.Width, proc.Height)
		if err != nil {
			lastErr = err
			d.logf("compare batch %d round %d/%d unparsable: %v", batch, round, maxRounds, err)
			continue
		}
		if len(dets) == len(spec.Regions) {
			forceSingleRegionID(spec, dets)
			if d.Cache != nil {
				if blob, err := json.Marshal(measureCacheEntry{Text: resp.Text, W: proc.Width, H: proc.Height}); err == nil {
					d.Cache.L2.Put(l2Key, blob)
				}
			}
			return dets, meta, nil
		}
		lastErr = fmt.Errorf("model located %d/%d regions", len(dets), len(spec.Regions))
		d.logf("compare batch %d round %d/%d partial: %d/%d", batch, round, maxRounds, len(dets), len(spec.Regions))
	}

	if lastErr == nil {
		lastErr = fmt.Errorf("no usable reply returned")
	}
	return nil, meta, fmt.Errorf(
		"Region location failed after %d round(s): %v. Check that the screenshot actually contains the regions named in the spec.",
		maxRounds, lastErr)
}
