# Marks: from a generated candidate to SVG

A raster mark is a candidate. The deliverable is an SVG that follows the brand kit's construction rules
(see the repo's `CLAUDE.md` and each imprint's `identity/<imprint>/README.md`): a 256 px document, 8 px grid,
the mark body on 24 units with 4 units of clearspace, filled paths only, strokes and type converted to paths.

## Rebuild as geometry (default)
Most of the family's marks are polygons on the grid, so rebuild them instead of tracing:

1. Read the selected cell. Name its construction: the shapes, which edges share the family's 2:1 diagonal,
   and the stroke contrast (heavy vs. hairline, in grid units).
2. Snap every vertex to the 8 px grid, or to a 4 px half-step where the contrast needs it, and write the
   SVG by hand with one `<path>` per element and ids matching the identity README.
3. Render it at 16, 24, 32, 64, and 512 (`inkscape <svg> --export-type=png --export-width=<n>`), and
   view it beside the Solo7 glyph and the sibling imprints' marks at the same size.
4. If it fills in at 16–24 px, derive `mark-small.svg`: the same drawing with hairlines doubled and gaps opened.
5. Record the vertex list in the identity README in the existing format (`V: (x,y) …`, weights, gaps).

## Trace (organic shapes only)
When the mark can't be described as grid geometry (a brushed glyph, an emblem):

1. Threshold the cell to 1-bit at its native size, then `potrace -s --turdsize 4 --alphamax 1.0 -o out.svg in.pbm`.
2. Scale it onto the 256 px document, align the bounding box to the clearspace, and simplify nodes until
   the 32 px render no longer changes.
3. Fill with the ink token's hex value, with no stroke.

## Wordmarks and lockups
Type is never generated. Set the wordmark in the imprint's locked typeface from `fonts/`, convert it to a
path (`inkscape --export-text-to-path`), and place it with the README's spacing rules. Generated sheets are
only for exploring custom letterforms. A selected custom letterform gets rebuilt as geometry like a mark.

## Provenance
In the identity README, below the mark's spec line: `Provenance: generated (art/<imprint>/source/<slot>.png,
<tool>, <date>, basis <AD v<n> | positioning.md @ <short commit>>), rebuilt as grid geometry` or `hand`.
The basis is the approved art direction if the imprint has one, and otherwise the commit of the
`positioning.md` whose direction statement the prompts used. A generated mark carries weaker IP protection
than a drawn one. Brian accepted that, but flag it before any trademark filing.

Locking is Brian's call. Write `LOCKED <date>` only after he says so.
