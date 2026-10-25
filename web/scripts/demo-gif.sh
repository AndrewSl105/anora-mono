#!/bin/sh
# Turns the demo video recorded by e2e/screenshots (test-results/demo.webm) into
# ../docs/screenshots/demo.gif: 12 fps, 390 px wide, one shared palette.
# Uses $FFMPEG, or ffmpeg from PATH; skips the GIF when neither is there.
set -e
cd "$(dirname "$0")/.."
FFMPEG=${FFMPEG:-ffmpeg}
if ! command -v "$FFMPEG" >/dev/null 2>&1; then
  echo "demo-gif: ffmpeg not found, skipping docs/screenshots/demo.gif" >&2
  exit 0
fi
"$FFMPEG" -loglevel error -y -i test-results/demo.webm -filter_complex \
  "fps=12,scale=390:-1:flags=lanczos,split[a][b];[a]palettegen=max_colors=128:stats_mode=diff[p];[b][p]paletteuse=dither=bayer:bayer_scale=5:diff_mode=rectangle" \
  ../docs/screenshots/demo.gif
ls -l ../docs/screenshots/demo.gif
