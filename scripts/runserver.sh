#!/bin/bash

# check if the config file exists - if not then create a default one
"$SNAP/bin/influxproxy" -c "$SNAP_DATA/config.toml" config check --write-new

# now actually run the server - if it fails to run then sleep for 5 seconds
# to attempt to wait for edgexfoundry to come online
if ! "$SNAP/bin/influxproxy" --debug -c "$SNAP_DATA/config.toml" start ; then
    sleep 5
fi
