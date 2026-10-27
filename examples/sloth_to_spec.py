"""Reference converter: Sloth parent-children.html → compare_design spec JSON v1.

Designed around how Sloth actually emits:
- Tailwind arbitrary-value classes encode position/size/color (e.g. left-[17px]
  w-[732px] text-[#794633]).
- Each DOM node carries data-id (splits into inst/master IDs; the raw-tree's
  id uses the inst part only, e.g. "37:40674").
- TEXT nodes put their visible text in the HTML text content directly.
- SOLID fills are represented as background-color or explicit bg- classes;
  image fills use bg-[url(...)] (not converted to colour).

This script is deliberately minimal: extract bbox + one dominant solid colour
+ text. A production converter can extend it to gradients, multi-fill blends,
strokes, etc.

Usage:
    python examples/sloth_to_spec.py path/to/parent-children.html \
        --output width,height \
        [--max-nodes N] \
        [--selector "div[data-type='TEXT']"]
"""

import argparse
import re
import sys
from html.parser import HTMLParser
from typing import Optional


# Tailwind arbitrary value regexes. They appear as `name-[value]` in class lists.
# Captures the inner value string (may contain spaces like `rgb(255, 182, 80)`).
_ARBITRARY = re.compile(r'([a-z_-]+)-\[(.+?)\](?=\s|$)', re.I)

# Named Tailwind helpers (bg, text, left, top, w, h, font-size etc.) we want to
# keep; everything else is noise.
_COLOR_PATTERNS = re.compile(r'\[#[0-9a-fA-F]{3,8}|rgba?\([^)]+\)', re.I)


class SlothHTMLParser(HTMLParser):
    def __init__(self, *, width: Optional[int] = None, height: Optional[int] = None,
                 max_nodes: int = 200, selector: Optional[str] = None):
        super().__init__()
        self.width = width
        self.height = height
        self.max_nodes = max_nodes
        self.selector = selector  # e.g. "div[data-type='TEXT']"
        self.nodes = []
        self.depth = 0
        # stack of (tag, attrs, depth) for simple selector matching
        self._path: list[dict] = []

    def handle_starttag(self, tag, attrs):
        self.depth += 1
        d = dict(attrs)
        d['_tag'] = tag
        d['_depth'] = self.depth
        d['_idx'] = len(self.nodes)
        self._path.append(d)
        self._check_selector(d, tag)

    def handle_endtag(self, tag):
        self.depth -= 1
        while self._path and self._path[-1]['_tag'] == tag:
            self._path.pop()

    def _check_selector(self, d: dict, tag: str):
        if self.max_nodes <= 0 or not self.selector:
            return
        # Very simple selector: tag[data-attr='value'].
        m = re.match(r'^([a-zA-Z0-9]+)(\[([a-zA-Z_][\w-]*)=(["\'])(.+?)\4\])?$', self.selector)
        if not m:
            return
        sel_tag = m.group(1)
        if tag != sel_tag:
            return
        attr_m = m.group(3)
        if not attr_m:
            return
        val_m = m.group(5)
        if d.get(attr_m) != val_m:
            return
        if not (node := self._extract_node(d)):
            return
        if len(self.nodes) >= self.max_nodes:
            return
        self.nodes.append(node)

    def handle_data(self, data):
        if not self._path:
            return
        if not self.nodes:
            return
        last = self.nodes[-1]
        # Only push text content for leaf-ish nodes (no children captured yet).
        # We accumulate because parsers may yield fragmented text chunks.
        last['text'] = (last.get('text', '') or '') + data

    def _extract_node(self, d: dict) -> Optional[dict]:
        data_id = (d.get('data-id') or '').strip()
        if not data_id:
            return None
        # Keep only useful data-* keys; drop huge inline class lists after parse.
        cls = d.get('class', '')
        pos, size, color = _parse_tailwind(cls)
        text = (d.get('text') or '').strip()
        name = (d.get('data-name') or '').strip()
        data_type = (d.get('data-type') or '').strip()

        node: dict = {'data_id': data_id}
        if pos:
            node['pos'] = pos
        if size:
            node['size'] = size
        if color:
            node['color'] = color
        if text:
            node['text'] = text
        if name:
            node['name'] = name
        if data_type:
            node['type'] = data_type
        return node


def _parse_tailwind(cls: str) -> tuple[list[float], list[float], Optional[str]]:
    """Return (pos [l,t], size [w,h], first color #hex)."""
    matches = _ARBITRARY.findall(cls)
    pos: list[float] = []
    size: list[float] = []
    color: Optional[str] = None
    for name, value in matches:
        vn = value.strip()
        # Positions
        if name in ('left', 'top'):
            if vn.endswith('px'):
                try:
                    pos.append(float(vn[:-2]))
                except ValueError:
                    pass
            else:
                try:
                    pos.append(float(vn))
                except ValueError:
                    pass
        elif name in ('w', 'width'):
            if vn.endswith('px'):
                try:
                    size.append(float(vn[:-2]))
                except ValueError:
                    pass
        elif name in ('h', 'height'):
            if vn.endswith('px'):
                try:
                    size.append(float(vn[:-2]))
                except ValueError:
                    pass
        # Colors
        m = _COLOR_PATTERNS.match(vn)
        if m and color is None:
            c = m.group(0)
            if c.startswith('#'):
                color = c.lower()
            elif c.startswith('rgb'):
                color = _rgba_to_hex(c)
    return pos, size, color


def _rgba_to_hex(s: str) -> str:
    nums = re.findall(r'[\d.]+', s)
    if len(nums) < 3:
        return '#000000'
    r, g, b = [float(n) for n in nums[:3]]
    a = float(nums[3]) if len(nums) > 3 else 1.0
    if a < 0.5:
        return ''
    return '#%02X%02X%02X' % (int(r), int(g), int(b))


def _normalize_color(h: str) -> Optional[str]:
    # Expand 3-char to 6-char, lowercase.
    h = h.lower().strip()
    if h.startswith('#') and len(h) == 4:
        h = '#' + ''.join(c * 2 for c in h[1:])
    if re.match(r'^#[0-9a-f]{6}$', h):
        return h
    return None


def main():
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument('html', help='Sloth parent-children.html path')
    ap.add_argument('--output', nargs=2, metavar=('W', 'H'), help='image dimensions (px)')
    ap.add_argument('--max-nodes', type=int, default=200)
    ap.add_argument('--selector', default=None,
                    help='Simple selector, e.g. "div[data-type=TEXT]"')
    args = ap.parse_args()

    with open(args.html, encoding='utf-8') as f:
        raw = f.read()

    parser = SlothHTMLParser(
        width=int(args.output[0]) if args.output else None,
        height=int(args.output[1]) if args.output else None,
        max_nodes=args.max_nodes,
        selector=args.selector,
    )
    parser.feed(raw)

    regions: list[dict] = []
    for n in parser.nodes:
        pos = n.get('pos', [])
        size = n.get('size', [])
        if len(pos) < 2 or len(size) < 2:
            continue
        color = _normalize_color(n['color']) if n.get('color') else None
        text = n.get('text')
        if not color and not text:
            continue  # node without verifiable expectation is noise
        region: dict = {
            'id': n['data_id'],
            'desc': '%s %s' % (n.get('type', ''), n.get('name', '')),
            'bbox': [pos[0], pos[1], pos[0] + size[0], pos[1] + size[1]],
        }
        if color:
            region['color'] = color
        if text:
            region['text'] = text
        regions.append(region)

    out: dict = {'version': 1, 'regions': regions}
    if args.output:
        out['image'] = {'width': int(args.output[0]), 'height': int(args.output[1])}

    print('# generated by examples/sloth_to_spec.py')
    import json
    print(json.dumps(out, ensure_ascii=False, indent=2))


if __name__ == '__main__':
    main()
