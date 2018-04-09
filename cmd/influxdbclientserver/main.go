package main

import (
	"fmt"
	"math"
	"net"
	"strconv"
	"strings"
	"time"

	"bytes"
	"encoding/json"
	"io/ioutil"
	"net/http"
	"os"

	"github.com/edgexfoundry/edgex-go/core/domain/models"
	influx "github.com/influxdata/influxdb/client/v2"
	flags "github.com/jessevdk/go-flags"
)

const (
	edgeXCreateRESTRegistrationJSON = `{
	"name":"golang-server",
	"enable":true,
	"format":"JSON",
	"destination": "REST_ENDPOINT",
	"addressable": {
	    "name": "DesktopREST",
	    "protocol": "HTTP",
   	    "method": "POST", 
	    "address": "%s",
	    "port": %d,
	    "path": "/edgex"
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

	// edgex opts
	EdgeXRegisterRESTClient bool   `short:"r" long:"edgex-export-distro-rest" description:"Enable Edgex Export Distro registration"`
	EdgexDeleteRegistration bool   `short:"d" long:"edgex-export-distro-rest-clean" description:"Edgex Export Distro clean registration (delete existing registration before registering new one)"`
	EdgeXExportDistroHost   string `short:"e" long:"edgex-export-distro-host" description:"Edgex Export Distro hostname (for registering the REST client)"`
	EdgeXExportDistroPort   uint   `short:"f" long:"edgex-export-distro-port" description:"Edgex Export Distro port" default:"48071"`

	// http opts
	HTTPPort uint   `short:"h" long:"http-port" description:"HTTP server port to bind on" default:"8080"`
	HTTPHost string `short:"a" long:"http-host" description:"HTTP server hostname to bind on"`
}

// Execute of StartCmd will start running the web server
func (cmd *StartCmd) Execute(args []string) (err error) {
	// Confirm the HTTPPort isn't 0 (cases where it's less than 0 are handled by the flags library, as it's declared as a uint)
	if cmd.HTTPPort == 0 {
		return fmt.Errorf("invalid port number")
	}

	// Before starting the server, check if we need to register this REST server as a client with Edgex Distro
	if cmd.EdgeXRegisterRESTClient {
		edgexRegistrationEndpoint := fmt.Sprintf("http://%s:%d/api/v1/registration", cmd.EdgeXExportDistroHost, cmd.EdgeXExportDistroPort)
		// Since we're creating a new REST registration, check if we should delete the current registration first
		if cmd.EdgexDeleteRegistration {
			// Make a new client + DELETE request against the registration endpoint with the registration name directory
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

		// Because the export-distro service needs to make a network connection to this machine, and may be on a different machine than this one
		// we need to find the ip address that edgex should use when connecting to this machine dynamically, as the hostname specified on the command line
		// may be something like localhost, etc.
		// To handle this, we make a connection to the edgex distro host specified, and then look at the local address used by that socket connection
		// this also handles the case where there's some kind of proxy, but obviously that proxy needs to be setup to forward data from edgex to this server
		// but that's a whole different problem
		conn, err := net.Dial("udp", fmt.Sprintf("%s:%d", cmd.EdgeXExportDistroHost, cmd.EdgeXExportDistroPort))
		if err != nil {
			return err
		}

		// Cast the address to a UDP address to get the IP as a net.IP
		localAddr, ok := conn.LocalAddr().(*net.UDPAddr)

		// don't defer closing the connection because if successful, we never actually exit this function when we start listening for new HTTP connections
		conn.Close()

		if !ok {
			return fmt.Errorf("invalid address object: %+v", conn.LocalAddr())
		}

		// Format the registration json with the IP address we just found for this server and the specified port we bind on
		registerJSON := fmt.Sprintf(edgeXCreateRESTRegistrationJSON, localAddr.IP.String(), cmd.HTTPPort)
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
			return fmt.Errorf("failed to register REST endpoint with export-distro (status code : %d): %s", res.StatusCode, body)
		}
	}

	// start the HTTP server
	http.HandleFunc("/edgex", readingData)
	return http.ListenAndServe(fmt.Sprintf("%s:%d", cmd.HTTPHost, cmd.HTTPPort), nil)
}

func genNewClientID() string {
	// TODO: generate randomly
	return "unique"
}

func readingData(w http.ResponseWriter, req *http.Request) {
	// make sure that the method is a post
	if strings.ToUpper(req.Method) != "POST" {
		// error only post is supported, add Allow header with just POST
		// and add MethodNotAllowed response code
		w.Header().Add("Allow", "POST")
		w.WriteHeader(http.StatusMethodNotAllowed)
		_, err := w.Write([]byte("invalid request, only POST supported"))
		if err != nil {
			fmt.Fprintln(os.Stderr, "error writing error response")
		}
		return
	}

	// attempt to decode the body as an event JSON
	var event models.Event
	err := json.NewDecoder(req.Body).Decode(&event)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// now that we have the event, in another go routine
	// we send the event's readings to influxdb
	// this is because we don't need to block this http request with the result of sending
	// to influx
	go sendEventToInflux(event)
	w.WriteHeader(http.StatusOK)
}

// TODO: this should parse the message from edgex and save it into an in memory database, etc.
func sendEventToInflux(event models.Event) {
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

	// Add all readings into batch influxdb points
	for _, reading := range event.Readings {

		// Make the map of metadata for this reading (i.e. "tags" in influxdb parlance)
		tags := map[string]string{
			"id": reading.Id.Hex(),
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

// command parser
var parser = flags.NewParser(&cmd, flags.Default)

// empty - the command execution happens in *.Execute methods
func main() {
	_, err := parser.Parse()
	if err != nil {
		os.Exit(1)
	}
}
