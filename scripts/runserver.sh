#!/bin/sh
set -e

# check if the config file exists - if not then create a default one
"$SNAP/bin/influxproxy" -c "$SNAP_DATA/config.toml" config check --write-new

# now actually run the server
"$SNAP/bin/influxproxy" --debug -c "$SNAP_DATA/config.toml" start
