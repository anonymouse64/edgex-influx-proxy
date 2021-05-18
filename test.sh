#!/bin/bash -ex

# remove any previous snaps
sudo snap remove edgexfoundry \
    edgex-device-mqtt \
    mosquitto \
    edgex-influx-proxy \
    influxdb-ijohnson

if ! command -v jq > /dev/null; then
    sudo snap install jq
fi

sudo snap install mosquitto influxdb-ijohnson

# TODO: paramaterize the edgex channel to test with
TRACK=latest
sudo snap install edgexfoundry --channel="$TRACK/stable"

# install the version of the edgex-influx-proxy version under test
sudo snap install --dangerous "$@"

# don't use track for device service
sudo snap install edgex-device-mqtt

# create the influxdb database
influxdb-ijohnson.influx -execute "CREATE DATABASE edgex"

# wait for things to startup
sleep 10

# start device-mqtt
sudo snap start --enable edgex-device-mqtt

# wait for device-mqtt to settle
sleep 5

# send a command via MQTT with mosquitto_pub to device-mqtt
mosquitto_pub -t DataTopic -m '{"name":"MQTT test device","cmd":"randfloat32","randfloat32":"1.2"}'

# wait for it to propogate through the system
sleep 1

# check that the measurement shows up in influxdb

# TODO: this should return something that we can grep for, but currently doesn't
influxdb-ijohnson.influx -execute 'SELECT * from "MQTT test device"' -database=edgex
