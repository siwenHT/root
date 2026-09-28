#!/bin/bash
set -eu
# Keep the established node arguments and working directory in z_run.sh.
# Override with 200 to restore the original announcement grace period.
export ARB_TX_ARRIVE_TIMEOUT_MS="${ARB_TX_ARRIVE_TIMEOUT_MS:-25}"
exec bash /home/bsc/fullnode/z_run.sh
