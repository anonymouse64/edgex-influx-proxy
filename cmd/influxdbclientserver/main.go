package main

import (
	"fmt"
	"math"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"bytes"
	"encoding/json"
	"io/ioutil"
	"net/http"
	"os"

	MQTT "github.com/eclipse/paho.mqtt.golang"
	"github.com/edgexfoundry/edgex-go/core/domain/models"
	influx "github.com/influxdata/influxdb/client/v2"
	flags "github.com/jessevdk/go-flags"
)

const (
	edgeXCreateMQTTRegistrationJSON = `{
	"name":"golang-server",
	"enable":true,
	"format":"JSON",
	"destination": "MQTT_TOPIC",
	"addressable": {
	    "id": null,
	    "created": 0,
	    "modified": 0,
	    "origin": 0,
	    "name": "DesktopMQTT",
	    "protocol": "TCP",
	    "address": "%s",
	    "port": %d,
	    "path": null,
	    "publisher": "EdgeXExportPublisher",
	    "topic": "EdgeXDataTopic"
	  }
}`
	httpUnknownError = iota
	httpInvalidFormat
	httpInvalidName
	httpInvalidNumber
	httpInvalidReading
	httpPlotFailure
)

type DataValueType int

const (
	BoolType DataValueType = iota
	IntType
	FloatType
	StringType
)

// Command is the command for application management
type Command struct {
	Start StartCmd `command:"start" description:"Start the server"`
}

// HTTPErrorResponse is for errors to be returned via HTTP
type HTTPErrorResponse struct {
	Error string `json:"error_msg" yaml:"error_msg"`
	Code  int    `json:"error_code" yaml:"error_code"`
}

// The current input command
var cmd Command

// StartCmd command for creating an application
type StartCmd struct {
	// general opts
	Verbose bool `short:"v" long:"verbose" description:"Verbose output"`

	// mqtt opts
	MQTTPort       uint   `short:"m" long:"mqtt-port" description:"MQTT server port to connect to" default:"1883"`
	MQTTSSL        bool   `short:"s" long:"mqtt-ssl" description:"MQTT connection protocol (default no encryption)"`
	MQTTHost       string `short:"b" long:"mqtt-broker" description:"MQTT server hostname to connect to" default:"localhost"`
	MQTTClientName string `short:"c" long:"mqtt-client" description:"MQTT clientname to use (default is automatically generated)"`
	MQTTTopic      string `short:"t" long:"mqtt-topic" description:"MQTT topic name to subscribe on" default:"EdgeXDataTopic"`
	MQTTUsername   string `short:"u" long:"mqtt-user" description:"MQTT server username"`
	MQTTPassword   string `short:"p" long:"mqtt-passwd" description:"MQTT server password"`
	MQTTQoS        int    `short:"q" long:"mqtt-qos" choice:"0" choice:"1" choice:"2" description:"MQTT Quality Of Service for the topic"`
	MQTTSCertAuth  string `short:"i" long:"mqtt-cert" description:"MQTT secure certificate file"`

	// edgex opts
	EdgeXRegisterMQTT       bool   `short:"r" long:"edgex-export-distro-mqtt" description:"Enable Edgex Export Distro registration"`
	EdgexDeleteRegistration bool   `short:"d" long:"edgex-export-distro-mqtt-clean" description:"Edgex Export Distro clean registration (delete existing registration before registering new one)"`
	EdgeXExportDistroHost   string `short:"e" long:"edgex-export-distro-host" description:"Edgex Export Distro hostname (for registering the MQTT server)"`
	EdgeXExportDistroPort   uint   `short:"f" long:"edgex-export-distro-port" description:"Edgex Export Distro port" default:"48071"`

	// http opts
	HTTPPort uint   `short:"h" long:"http-port" description:"HTTP server port to bind on" default:"8080"`
	HTTPHost string `short:"a" long:"http-host" description:"HTTP server hostname to bind on" default:"0.0.0.0"`
}

// Execute of StartCmd will start running the web server
func (cmd *StartCmd) Execute(args []string) (err error) {

	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)

	client, err := setupMQTTClient(cmd)
	if err != nil {
		return err
	}
	defer client.Disconnect(0)

	// Before starting the server, check if we need to register the MQTT server with Edgex Distro
	if cmd.EdgeXRegisterMQTT {
		edgexRegistrationEndpoint := fmt.Sprintf("http://%s:%d/api/v1/registration", cmd.EdgeXExportDistroHost, cmd.EdgeXExportDistroPort)
		// since we're creating a new MQTT registration, we need to check if we should delete the current registration if it exists
		if cmd.EdgexDeleteRegistration {
			// make a new client + DELETE request against the registration endpoint with the registration name directory
			client := &http.Client{}
			req, err := http.NewRequest("DELETE", edgexRegistrationEndpoint+"/name/golang-server", nil)
			if err != nil {
				return err
			}
			_, err = client.Do(req)
			if err != nil {
				return err
			}
		}

		// Format the registration json with the mqtt host and the mqtt port
		registerJSON := fmt.Sprintf(edgeXCreateMQTTRegistrationJSON, cmd.MQTTHost, cmd.MQTTPort)
		// POST the request to export-client's registation endpoint with the formatted JSON as the body
		res, err := http.Post(
			edgexRegistrationEndpoint,
			"application/json",
			bytes.NewBufferString(registerJSON),
		)
		if err != nil {
			return err
		}

		if res.StatusCode != 201 {
			body, err := ioutil.ReadAll(res.Body)
			if err != nil {
				return err
			}
			return fmt.Errorf("failed to register mqtt broker with export-distro (status code : %d): %s", res.StatusCode, body)
		}
	}

	// Waits for signals
	<-c
	return err
}

func genNewClientID() string {
	// TODO: generate randomly
	return "unique"
}

// TODO: this should parse the message from edgex and save it into an in memory database, etc.
func onMessageReceived(client MQTT.Client, message MQTT.Message) {
	// Make a new HTTP client connection to influxdb
	influxClient, err := influx.NewHTTPClient(influx.HTTPConfig{
		Addr: "http://localhost:8086",
	})
	if err != nil {
		// TODO : setup an error channel and send this error to it
		fmt.Fprintf(os.Stderr, "error creating InfluxDB Client: %+v\n", err)
	}
	defer influxClient.Close()

	// Make a new set of batch points for this message
	bp, _ := influx.NewBatchPoints(influx.BatchPointsConfig{
		Database:  "edgex",
		Precision: "us",
	})

	var event models.Event
	err = json.Unmarshal(message.Payload(), &event)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error decoding message: %+v\n", err)
	} else {
		// Add all readings into batch influxdb points
		for _, reading := range event.Readings {

			// Make the map of metadata for this reading (i.e. "tags" in influxdb parlance)
			tags := map[string]string{
			// "id": reading.Id.Hex(),
			}

			// Make a map for the reading values (i.e "fields" in influxdb parlance) and
			// dynamically attempt to parse the type of value for this reading - edgex returns
			// all reading types as strings, so we have to do a little bit of guesswork to
			// figure out the actual underlying value type
			// For the field name, we use the reading name here
			fields := make(map[string]interface{})
			readingType, boolVal, floatVal, intVal := parseValueType(reading.Value)
			switch readingType {
			case BoolType:
				fields[reading.Name] = boolVal
			case IntType:
				fields[reading.Name] = intVal
			case FloatType:
				fields[reading.Name] = floatVal
			case StringType:
				fields[reading.Name] = reading.Value
			}

			// Calculate the unix time from the origin time in the reading
			unixTime := float64(reading.Origin) / 1000.0
			unixTimeSec := math.Floor(unixTime)
			unixTimeNSec := int64((unixTime - unixTimeSec) * 1000000000.0)

			// Make the reading point for this device
			pt, err := influx.NewPoint(
				reading.Device,
				tags,
				fields,
				// need to make sure the Time value returned is in UTC - but note we don't have to convert it before hand
				// because Unix time is always in UTC, but time.Time is in the local timezone
				time.Unix(int64(unixTimeSec), unixTimeNSec),
			)
			if err != nil {
				// TODO : send error via channel
				fmt.Fprintf(os.Stderr, "error creating reading point: %+v\n", err)
			}

			// Add it to the batch set
			bp.AddPoint(pt)
		}

		// finally write all these points out to influx
		err = influxClient.Write(bp)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error writing points to influx: %+v\n", err)
		}
	}
}

// parseValueType attempts to parse the value of the string value into a proper go type
func parseValueType(valueStr string) (typeStr DataValueType, boolVal bool, floatVal float64, intVal int64) {
	// first check for boolean
	fixedStr := strings.TrimSpace(strings.ToLower(valueStr))
	if fixedStr == "true" {
		typeStr = BoolType
		boolVal = true
		return
	} else if fixedStr == "false" {
		typeStr = BoolType
		boolVal = false
		return
	}

	// check for integer
	intVal, err := strconv.ParseInt(fixedStr, 10, 64)
	if err == nil {
		// then it's an int value
		typeStr = IntType
		return
	}

	// finally check for a floating point value
	floatVal, err = strconv.ParseFloat(fixedStr, 64)
	if err == nil {
		// success, value is a float
		typeStr = FloatType
		return
	}

	// if we get here, it's not any scalar numeric value, so just assume it's meant as a string
	typeStr = StringType
	return
}

// mqtt disconnect callback
func onDisconnect(client MQTT.Client, err error) {
	fmt.Printf("client disconnected: %+v\n", err)
}

// mqtt connect callback
func onConnect(client MQTT.Client) {
	fmt.Println("client connected")
}

// command parser
var parser = flags.NewParser(&cmd, flags.Default)

// empty - the command execution happens in *.Execute methods
func main() {
	_, err := parser.Parse()
	if err != nil {
		os.Exit(1)
	}
}
