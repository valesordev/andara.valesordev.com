# Building a slot

`make <imprint>` rebuilds every slot output from its source. The rule's prerequisite is the source
file itself, so replacing or editing the source triggers a rebuild, and a clean build reproduces the outputs.

| Source | Export step (into `dist/<imprint>/art/src/<slot>.png`) |
|---|---|
| `<slot>.png` (generated, or a hand-made PNG) | none: the treatment reads the source |
| `<slot>.svg` | `inkscape $< --export-type=png --export-width=<canvas W> --export-filename=$@` |
| `<slot>.kra` | `krita --export --export-filename $@ $<` (headless; needs Krita on the build machine) |

The slot file's spec gives the canvas width. Brian's Krita brief keeps paper and sketch layers hidden
before saving, so the export is ink only. If a `.kra` exports with them visible, ask him to hide them and
save. Don't add layer surgery to the build.

Pattern, added once per slot under the imprint's rules:

```make
valesordev: dist/valesordev/art/hero.png

# hand-made source: export, then treat
dist/valesordev/art/src/hero.png: art/valesordev/source/hero.kra
	@mkdir -p $(dir $@)
	krita --export --export-filename $@ $<
dist/valesordev/art/hero.png: dist/valesordev/art/src/hero.png art/treat/ink.py
	python3 art/treat/ink.py $< $@

# generated (or PNG) source: treat directly
# dist/valesordev/art/hero.png: art/valesordev/source/hero.png art/treat/ink.py
#	python3 art/treat/ink.py --moon 491,260,160.5 $< $@
```

A swap changes only these lines, and the output path stays the same.
