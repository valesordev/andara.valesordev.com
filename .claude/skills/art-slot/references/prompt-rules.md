# Prompt rules

The imprint's ChatGPT project instructions carry the art direction. A prompt carries only what's specific
to the slot and the candidate.

Each block, in this order:

1. **Request line:** `<slot> · candidate <letter> · AD v<n>`. Brian matches files to candidates by it.
2. **Format:** aspect ratio and orientation from the spec (the generator picks the pixels; treatment
   and the build resize). State where the empty zone is: "keep the left third nearly empty".
3. **Subject:** what's in the image, concrete nouns first, then position and scale.
4. **Thesis:** the candidate's one-line variation, in visual terms.
5. **Keep:** when reference files are attached, name each and the features to keep consistent with it.
6. **Not:** at most three slot-specific exclusions. The project instructions already hold the general ones.

Write for the treatment. If the treatment thresholds to ink, ask for clean black line on a plain light
ground with no tone washes. If it maps to two tokens, ask for two flat values. The easier the
treatment's job, the less the result looks generated.

**Mark sheets** (kind `mark`): ask for a sheet of 9–12 small variations on one axis, flat black on white,
no text, no 3D, no gradients, each mark in its own cell. Describe the construction (grid, the 2:1
diagonal shared by the family's marks, stroke contrast) instead of a style. The selected cell gets rebuilt
as geometry (`marks.md`), so precision doesn't matter here. Proportion and idea do.

Block format:

````text
```text
<slot> · candidate <letter> · AD v<n>
Format: <aspect>, <orientation>. <empty zone>.
Subject: <…>
Variation: <thesis>
Keep: <file> — <features>   (omit when no references)
Not: <…>
```
Save as: art/<imprint>/candidates/<slot>-<letter>.png · Attach: <files or "none">
````
