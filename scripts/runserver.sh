#!/bin/sh
set -e

# check if the config file exists - if not then create a default one
$SNAP/bin/influxdbclientserver -c $SNAP_DATA/config.toml config check --write-new

# now actually run the server
$SNAP/bin/influxdbclientserver -c $SNAP_DATA/config.toml start 