package main

import (
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/edgexfoundry/app-functions-sdk-go/appcontext"
	"github.com/edgexfoundry/app-functions-sdk-go/appsdk"
	"github.com/edgexfoundry/go-mod-core-contracts/models"
	influx "github.com/influxdata/influxdb1-client/v2"
)

const (
	serviceKey = "edgex-influx-proxy"
	Version    = "1.0.0"
)

func main() {
	// create the SDK with the service key
	edgexSdk := &appsdk.AppFunctionsSDK{ServiceKey: serviceKey}
	err := edgexSdk.Initialize()
	if err != nil {
		edgexSdk.LoggingClient.Error(fmt.Sprintf("SDK initialization failed: %v\n", err))
		os.Exit(-1)
	}

	// get the app service configuration
	influxConfig := influx.HTTPConfig{}
	ptConfig := influx.BatchPointsConfig{}
	if appSettings := edgexSdk.ApplicationSettings(); appSettings != nil {
		// check for the hostname, default to localhost
		influxHost, ok := appSettings["InfluxDBHost"]
		if !ok {
			edgexSdk.LoggingClient.Info("missing value for \"InfluxDBHost\", defaulting to \"localhost\"")
			influxHost = "localhost"
		}

		// check for the port, default to 8086
		var influxPort uint64
		influxPortStr, ok := appSettings["InfluxDBPort"]
		if ok {
			influxPort, err = strconv.ParseUint(influxPortStr, 10, 64)
			if err != nil || influxPort == 0 {
				edgexSdk.LoggingClient.Error(fmt.Sprintf("Invalid \"InfluxDBPort\" setting of %s, must be integer greater than 0", influxPortStr))
				os.Exit(-1)
			}
		} else {
			edgexSdk.LoggingClient.Info("missing value for \"InfluxDBPort\", defaulting to 8086")
			influxPort = 8086
		}

		// set the address for the config
		influxConfig.Addr = fmt.Sprintf(
			"http://%s:%d",
			influxHost,
			influxPort,
		)

		// if the username is specified and non-empty use it
		influxUser, ok := appSettings["InfluxDBUsername"]
		if ok && influxUser != "" {
			influxConfig.Username = influxUser
		}

		// if the password is specified and non-empty use it
		influxPassword, ok := appSettings["InfluxDBPassword"]
		if ok && influxPassword != "" {
			influxConfig.Password = influxPassword
		}

		// require the database name to insert to
		ptConfig.Database, ok = appSettings["InfluxDBDatabaseName"]
		if !ok {
			edgexSdk.LoggingClient.Error("missing value for \"InfluxDBDatabaseName\"")
			os.Exit(-1)
		}

		// require the database precision to use for the database
		ptConfig.Precision, ok = appSettings["InfluxDBDatabasePrecision"]
		if !ok {
			edgexSdk.LoggingClient.Error("missing value for \"InfluxDBDatabasePrecision\"")
			os.Exit(-1)
		}
	} else {
		edgexSdk.LoggingClient.Error("No application settings found")
		os.Exit(-1)
	}

	// Make a new HTTP client connection to influxdb
	influxClient, err := influx.NewHTTPClient(influxConfig)
	if err != nil {
		edgexSdk.LoggingClient.Error("No application settings found")
		os.Exit(-1)
	}

	// close the client once the function returns, as we don't return from
	// this function unless error, but we will keep using the influx client
	// until an error happens
	defer influxClient.Close()

	// the only function in the pipeline is to send it to influxDB
	// TODO: allow filtering by device name from the configuration.toml file
	err = edgexSdk.SetFunctionsPipeline(
		sendToInfluxDBFunc(influxClient, ptConfig),
	)
	if err != nil {
		edgexSdk.LoggingClient.Error(fmt.Sprintf("%s", err))
		os.Exit(-1)
	}

	// run the SDK service
	err = edgexSdk.MakeItRun()
	if err != nil {
		edgexSdk.LoggingClient.Error("MakeItRun returned error: ", err.Error())
		os.Exit(-1)
	}

	os.Exit(0)
}

// sendToInfluxDB sends each data event to InfluxDB as a point
func sendToInfluxDBFunc(influxClient influx.Client, ptConfig influx.BatchPointsConfig) func(edgexcontext *appcontext.Context, params ...interface{}) (bool, interface{}) {
	return func(edgexcontext *appcontext.Context, params ...interface{}) (bool, interface{}) {
		if len(params) < 1 {
			// We didn't receive a result
			return false, errors.New("No Data Received")
		}

		for _, obj := range params {
			event, ok := obj.(models.Event)
			if !ok {
				continue
			}

			// Make a new set of batch points for this event
			bp, err := influx.NewBatchPoints(ptConfig)
			if err != nil {
				edgexcontext.LoggingClient.Warn(fmt.Sprintf("%s", err))
			}

			for _, reading := range event.Readings {
				// TODO: use core-metadata to figure out the real Type instead
				// of guessing like this

				// parse the reading value string into a go type to be send to
				// influxdb
				fields := make(map[string]interface{})
				readingType, boolVal, floatVal, intVal := parseValueType(reading.Value)
				switch readingType {
				case boolType:
					fields[reading.Name] = boolVal
				case intType:
					fields[reading.Name] = intVal
				case floatType:
					fields[reading.Name] = floatVal
				case stringType:
					fields[reading.Name] = reading.Value
				}

				// Calculate the unix time from the origin time in the reading
				// note that the origin time is in milliseconds
				unixTime := float64(reading.Origin) / float64(time.Second/time.Nanosecond)
				unixTimeSec := math.Floor(unixTime)
				unixTimeNSec := int64((unixTime - unixTimeSec) * float64(time.Second/time.Nanosecond))

				// Make the point for this reading with the name as the device
				// it originated
				pt, err := influx.NewPoint(
					reading.Device,
					map[string]string{
						"id": reading.Id,
					},
					fields,
					// need to make sure the Time value returned is in UTC -
					// but note we don't have to convert it before hand
					// because Unix time is always in UTC, but time.Time is in
					// the local timezone
					time.Unix(int64(unixTimeSec), unixTimeNSec),
				)
				if err != nil {
					// TODO : send error via channel
					log.Printf("error creating reading point: %+v\n", err)
				}

				// Add it to the batch set
				bp.AddPoint(pt)
			}

			// finally write all these points out to influx
			err = influxClient.Write(bp)
			if err != nil {
				log.Printf("error writing points to influx: %+v\n", err)
			}
		}

		return true, nil
	}
}

// dataValueType is used when parsing the string Value out from a Reading
type dataValueType int

const (
	boolType dataValueType = iota
	intType
	floatType
	stringType
)

// parseValueType attempts to parse the value of the string value into a
// proper go type
func parseValueType(valueStr string) (typeStr dataValueType, boolVal bool, floatVal float64, intVal int64) {

	// first check for boolean
	// NOTE: string values of true/false that aren't boolean currently will
	// become booleans
	fixedStr := strings.TrimSpace(strings.ToLower(valueStr))
	if fixedStr == "true" {
		typeStr = boolType
		boolVal = true
		return
	} else if fixedStr == "false" {
		typeStr = boolType
		boolVal = false
		return
	}

	// check for base-10 signed integer
	intVal, err := strconv.ParseInt(fixedStr, 10, 64)
	if err == nil {
		// then it's an int value
		typeStr = intType
		return
	}

	// check for a floating point value encoded as base64
	data, err := base64.StdEncoding.DecodeString(valueStr)
	if err == nil {
		switch len(data) {
		case 4:
			// float 32
			typeStr = floatType
			bits := binary.BigEndian.Uint32(data)
			floatVal = float64(math.Float32frombits(bits))
			return
		case 8:
			// float 64
			typeStr = floatType
			bits := binary.BigEndian.Uint64(data)
			floatVal = math.Float64frombits(bits)
			return
		}
	}

	// if we get here, it's not any scalar numeric value, so just assume it's meant as a string
	typeStr = stringType
	return
}
