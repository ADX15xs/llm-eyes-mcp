package probe

import "testing"

func TestScore(t *testing.T) {
	// One target suffices: IoUMean always divides by len(Targets), so a lone
	// perfect region yields 1/8.
	gt := map[string][4]float64{"r1": {0.1, 0.1, 0.2, 0.2}}
	tests := []struct {
		name  string
		reply *Reply
		want  Result
	}{
		{
			name:  "nil reply scores zero",
			reply: nil,
			want:  Result{},
		},
		{
			name:  "perfect box and text",
			reply: &Reply{Regions: []Region{{ID: "r1", BBox: []float64{0.1, 0.1, 0.2, 0.2}, Text: "Gift Name"}}},
			want:  Result{Returned: 1, IoUMean: 1.0 / 8, IoUOk: 1, TextOk: 1},
		},
		{
			name:  "pixel-space reply is rescaled and flagged",
			reply: &Reply{Regions: []Region{{ID: "r1", BBox: []float64{75, 45, 150, 90}, Text: "gift name"}}},
			want:  Result{Returned: 1, IoUMean: 1.0 / 8, IoUOk: 1, TextOk: 1, PixelSpace: 1},
		},
		{
			name:  "disjoint box keeps text but no IoU",
			reply: &Reply{Regions: []Region{{ID: "r1", BBox: []float64{0.5, 0.5, 0.6, 0.6}, Text: "Gift Name"}}},
			want:  Result{Returned: 1, TextOk: 1},
		},
		{
			name: "missing region and malformed bbox count as IoU 0",
			reply: &Reply{Regions: []Region{
				{ID: "rx", BBox: []float64{0, 0, 0, 0}},
				{ID: "r1", BBox: []float64{0.1, 0.1, 0.2}},
			}},
			want: Result{Returned: 2},
		},
		{
			name:  "inverted corners degrade to low IoU instead of panicking",
			reply: &Reply{Regions: []Region{{ID: "r1", BBox: []float64{0.2, 0.2, 0.1, 0.1}, Text: "Gift Name"}}},
			want:  Result{Returned: 1, TextOk: 1},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Score(gt, tt.reply); got != tt.want {
				t.Errorf("Score = %+v, want %+v", got, tt.want)
			}
		})
	}
}
