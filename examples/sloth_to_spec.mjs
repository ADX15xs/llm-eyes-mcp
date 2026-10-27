#!/usr/bin/env node
// Sloth parent-children.html → compare_design spec JSON v1 (Node port)
// Uses only built-in modules; no external deps.

import fs from 'node:fs';
import path from 'node:path';

// Parse arguments
let filePath = null;
let width = null;
let height = null;
let maxNodes = 200;
let selector = null;
let outFile = null;

for (let i = 2; i < process.argv.length; i++) {
  const arg = process.argv[i];
  if (arg === '--output' && i + 2 < process.argv.length) {
    width = parseInt(process.argv[++i]);
    height = parseInt(process.argv[++i]);
  } else if (arg === '--max-nodes' && i + 1 < process.argv.length) {
    maxNodes = parseInt(process.argv[++i]);
  } else if (arg === '--selector' && i + 1 < process.argv.length) {
    selector = process.argv[++i];
  } else if (arg === '--out' && i + 1 < process.argv.length) {
    outFile = process.argv[++i];
  } else if (!filePath) {
    filePath = arg;
  }
}

if (!filePath) {
  console.error('Usage: sloth_to_spec.mjs <parent-children.html> [--output W H] [--max-nodes N] [--selector "div[data-type=\"TEXT\"]"]');
  process.exit(1);
}

function parsePx(value) {
  if (typeof value !== 'string') return NaN;
  const match = value.match(/([-+]?\d*\.?\d+)px?/);
  return match ? parseFloat(match[1]) : NaN;
}

function parseTailwind(cls) {
  if (!cls) return { pos: null, size: null, color: null };

  const pos = [];
  const size = [];
  let color = null;

  // Match Tailwind arbitrary values like left-[165px] or text-[#794633]
  const re = /([a-zA-Z_-]+)-\[([^\]]+)\]/g;
  let m;
  while ((m = re.exec(cls)) !== null) {
    const key = m[1].toLowerCase();
    const value = m[2];

    if (key === 'left' || key === 'top') {
      const parsed = parsePx(value);
      if (!isNaN(parsed)) pos.push(parsed);
    } else if (key === 'w' || key === 'width') {
      const parsed = parsePx(value);
      if (!isNaN(parsed)) size.push(parsed);
    } else if (key === 'h' || key === 'height') {
      const parsed = parsePx(value);
      if (!isNaN(parsed)) size.push(parsed);
    } else if (key.includes('text') || key.includes('bg')) {
      const colorMatch = value.match(/#[0-9a-fA-F]{3,8}/);
      if (colorMatch) {
        let hex = colorMatch[0].toLowerCase();
        if (hex.length === 4) {
          hex = '#' + hex[1] + hex[1] + hex[2] + hex[2] + hex[3] + hex[3];
        }
        if (hex.length === 7) color = hex;
      }
    }
  }

  return {
    pos: pos.length >= 2 ? [pos[0], pos[1]] : null,
    size: size.length >= 2 ? [size[0], size[1]] : null,
    color
  };
}

function extractTextBetweenTags(html, startId, endTag) {
  // Find the opening tag with this data-id, then extract text until closing tag
  const idRegex = new RegExp(`data-id=["']${startId.replace(/[-[\]{}()*+?.,\\^$|#\s]/g, '\\$&')}["']\\s*>`, 'i');
  const startMatch = html.match(idRegex);
  if (!startMatch) return '';

  const startPos = startMatch.index + startMatch[0].length;
  const endRegex = new RegExp(`<${endTag}\\s*>`, 'i');
  const endMatch = html.slice(startPos).match(endRegex);
  if (!endMatch) return '';

  const innerHTML = html.slice(startPos, startPos + endMatch.index);
  return innerHTML.replace(/<[^>]*>/g, ' ').replace(/\s+/g, ' ').trim();
}

function matchesSelector(node, sel) {
  if (!sel) return true;

  // Support simple selectors like "div[data-type=TEXT]"
  const tagMatch = sel.match(/^(\w+)(?:\[(\w[\w-]*)=["']?([^"'\]]+)["']?\])?$/);
  if (!tagMatch) return true;

  const [, tag, attr, val] = tagMatch;
  // Sloth exports are all <div> with data-type attributes, so we only enforce
  // the attribute check. The HTML tag part of the selector is advisory.
  if (attr && node[attr] !== val) return false;

  return true;
}

function main() {
  try {
    const html = fs.readFileSync(filePath, 'utf8');

    // Match all opening tags with data-id
    const openTagRe = /<([a-zA-Z][a-zA-Z0-9]*)\b([^>]*?)>(?=[^<]|$)/gi;
    const nodes = [];
    let m;

    while ((m = openTagRe.exec(html)) !== null) {
      const tag = m[1];
      const attrsStr = m[2];

      // Extract data-id
      const idMatch = attrsStr.match(/data-id=["']([^"']+)["']/);
      if (!idMatch) continue;
      const dataId = idMatch[1];

      // Extract other attributes
      const dataNameMatch = attrsStr.match(/data-name=["']([^"']*)["']/);
      const dataTypeMatch = attrsStr.match(/data-type=["']([^"']*)["']/);
      const classMatch = attrsStr.match(/class=["']([^"']*)["']/);

      const dataName = dataNameMatch ? dataNameMatch[1] : '';
      const dataType = dataTypeMatch ? dataTypeMatch[1] : '';
      const classAttr = classMatch ? classMatch[1] : '';

      const tailwind = parseTailwind(classAttr);

      const node = {
        'data-id': dataId,
        'data-name': dataName,
        'data-type': dataType,
        'tag': tag, // actual HTML tag from the opening tag match
        'pos': tailwind.pos,
        'size': tailwind.size,
        'color': tailwind.color,
        'text': ''
      };

      if (matchesSelector(node, selector)) {
        nodes.push(node);
      }
    }

    // Extract text content for nodes by finding text between opening/closing tags
    for (const node of nodes) {
      const escapedId = node['data-id'].replace(/[-[\]{}()*+?.,\\^$|#\s]/g, '\\$&');
      // Use the ACTUAL HTML tag (div/img/span), not data-type — Sloth puts
      // IMG/TEXT/CONTAINER in data-type, but every node is a real <div>/<img>.
      const tag = node['tag'] || 'div';
      // Self-closing elements have no text content
      if (['img', 'br', 'input', 'meta', 'link'].includes(tag)) continue;
      // Match opening tag - data-id can appear anywhere in attributes
      const openRe = new RegExp(`<${tag}\\b[^>]*data-id=["']${escapedId}["'][^>]*>`, 'i');
      const openMatch = html.match(openRe);
      if (!openMatch) continue;

      const startIdx = openMatch.index + openMatch[0].length;
      // Find matching closing tag by counting depth
      const closeRe = new RegExp(`</${tag}>`, 'gi');
      let depth = 1;
      let searchFrom = startIdx;
      while (depth > 0) {
        closeRe.lastIndex = searchFrom;
        const closeMatch = closeRe.exec(html);
        if (!closeMatch) break;
        const pos = closeMatch.index;
        // Count nested same-tag openings between start and this close
        const between = html.slice(startIdx, pos);
        const nestedOpen = (between.match(new RegExp(`<${tag}\\b[^>]*>`, 'gi')) || []).length;
        const nestedClose = (between.match(new RegExp(`</${tag}>`, 'gi')) || []).length;
        // new_depth = current_depth - 1 (close at pos) + nested_open - nested_close
        depth = depth - 1 + nestedOpen - nestedClose;
        if (depth <= 0) {
          const innerHTML = html.slice(startIdx, pos);
          node.text = innerHTML.replace(/<[^>]*>/g, ' ').replace(/\s+/g, ' ').trim();
          break;
        }
        searchFrom = pos + closeMatch[0].length;
      }
    }

    const filteredNodes = nodes.slice(0, maxNodes);

    const regions = [];
    for (const node of filteredNodes) {
      if (!node.pos || !node.size) continue;

      const bbox = [
        node.pos[0],
        node.pos[1],
        node.pos[0] + node.size[0],
        node.pos[1] + node.size[1]
      ];

      if (!node.color && !node.text) continue;

      const region = {
        id: node['data-id'],
        desc: `${node['data-type']} ${node['data-name']}`.trim(),
        bbox
      };

      if (node.color) region.color = node.color;
      if (node.text) region.text = node.text;

      regions.push(region);
    }

    const spec = {
      version: 1,
      regions
    };

    if (width && height) {
      spec.image = { width, height };
    }

    const blob = JSON.stringify(spec, null, 2) + '\n';
    if (outFile) {
      fs.writeFileSync(outFile, blob, 'utf8');
      console.error(`wrote ${regions.length} regions to ${outFile}`);
    } else {
      process.stdout.write(blob);
    }
  } catch (error) {
    console.error('Error:', error.message);
    process.exit(1);
  }
}

main();
