#!/usr/bin/env bash
# Runs one of AndRho's R analyses on the VPS for andrho-api's insights
# pipeline (internal/rstats, over the same SSH as odoo/provision-client.sh).
#   bash stats/run-r.sh <name>   < input.json   > output.json
# <name> is web_anova or seo_ranks (stats/<name>.R, next to this script).
# Needs r-base-core and r-cran-jsonlite. Install/update from the andrho-api
# repo with:  install -m 644 stats/*.R stats/run-r.sh /opt/andrho/VPS/stats/
set -euo pipefail
name="${1:-}"
[[ "$name" =~ ^[a-z0-9_]+$ ]] || { echo "run-r.sh: bad analysis name" >&2; exit 2; }
dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
[[ -f "$dir/$name.R" ]] || { echo "run-r.sh: no $name.R in $dir" >&2; exit 2; }
# Bounded: a stuck model must not pile up processes on the VPS.
exec timeout 60 nice -n 10 Rscript --vanilla "$dir/$name.R"
