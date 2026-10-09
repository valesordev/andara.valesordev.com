# Building a slot

`make <slug>` rebuilds every slot output from its source. The rule's prerequisite is the source
file itself, so replacing or editing the source triggers a rebuild, and a clean build reproduces the outputs.

| Source | Export step (into `brands/<slug>/dist/art/src/<slot>.png`) |
|---|---|
| `<slot>.png` (generated, or a hand-made PNG) | none: the treatment reads the source |
| `<slot>.svg` | `inkscape $< --export-type=png --export-width=<canvas W> --export-filename=$@` |
| `<slot>.kra` | `krita --export --export-filename $@ $<` (headless; needs Krita on the build machine) |

The slot file's spec gives the canvas width. Brian's Krita brief keeps paper and sketch layers hidden
before saving, so the export is ink only. If a `.kra` exports with them visible, ask him to hide them and
save. Don't add layer surgery to the build.

Pattern, added once per slot to `brands/<slug>/brand.mk`, which the root Makefile includes (see the
skill's Install step):

```make
V := brands/valesordev
valesordev: $(V)/dist/art/hero.png

# hand-made source: export, then treat
$(V)/dist/art/src/hero.png: $(V)/assets/illustration/hero-wide/hero.kra
	@mkdir -p $(dir $@)
	krita --export --export-filename $@ $<
$(V)/dist/art/hero.png: $(V)/dist/art/src/hero.png brands/_tools/treat/ink.py
	python3 brands/_tools/treat/ink.py $< $@

# generated (or PNG) source: treat directly
# $(V)/dist/art/hero.png: $(V)/assets/illustration/hero-wide/hero.png brands/_tools/treat/ink.py
#	python3 brands/_tools/treat/ink.py --moon 491,260,160.5 $< $@
```

A swap changes only these lines, and the output path stays the same.
