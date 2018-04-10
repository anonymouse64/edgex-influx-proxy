package main

import (
	"fmt"
	"log"
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

	"github.com/anonymouse64/edgex-web-demo/config"
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
	Start      StartCmd  `command:"start" description:"Start the server"`
	Config     ConfigCmd `command:"config" description:"Change or get config values"`
	ConfigFile string    `short:"c" long:"config-file" description:"Configuration file to use" required:"yes"`
}

// The current input command
var currentCmd Command

// ConfigCmd is for a set of commands working with the config file programmatically
type ConfigCmd struct {
	Check CheckConfigCmd `command:"check" descripttion:"Check a configuration file"`
	Set   SetConfigCmd   `command:"set" description:"Set configuration values in a file"`
	Get   GetConfigCmd   `command:"get" description:"Get configuration values from a file"`
}

// SetConfigCmd is a command for setting config values in the config file
type SetConfigCmd struct {
	Args struct {
		Key   string `positional-arg-name:"key"`
		Value string `positional-arg-name:"value"`
	} `positional-args:"yes" required:"yes"`
}

// Execute of SetConfigCmd will set config values from the command line inside the config file
// TODO: not implemented yet
func (cmd *SetConfigCmd) Execute(args []string) (err error) {
	// Load the config file
	err = config.LoadConfig(cmd.ConfigFile)
	if err != nil {
		return
	}

	// Now get the keys into the struct to set the values in the struct

	return
}

// GetConfigCmd is a command for getting config values from the config file
type GetConfigCmd struct {
	Args struct {
		Key string `positional-arg-name:"key"`
	} `positional-args:"yes" required:"yes"`
}

// Execute of GetConfigCmd will print off config values from the command line as specified in the config file
// TODO: not implemented yet
func (cmd *GetConfigCmd) Execute(args []string) (err error) {
	// Load the config file
	err = config.LoadConfig(cmd.ConfigFile)
	if err != nil {
		return
	}

	return
}

type CheckConfigCmd struct {
	WriteNewFile bool `short:"w" long:"write-new" description:"Whether to write a new config if the specified file doesn't exist"`
}

// Execute of CheckConfigCmd checks if the specified config file exists, and if it doesn't it creates one
//
func (cmd *CheckConfigCmd) Execute(args []string) (err error) {
	// first check if the specified file exists
	if _, err = os.Stat(currentCmd.ConfigFile); os.IsNotExist(err) {
		// file doesn't exist
		if cmd.WriteNewFile {
			// write out a new file then
			return config.WriteConfig(file, nil)
		} else {
			return fmt.Errorf("config file %s doesn't exist", currentCmd.ConfigFile)
		}
	}
	// otherwise the file exists, so load it
	return config.LoadConfig(currentCmd.ConfigFile)
}

// StartCmd command for creating an application
type StartCmd struct{}

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

	// Make a new HTTP client connection to influxdb
	influxClient, err := influx.NewHTTPClient(influx.HTTPConfig{
		Addr: fmt.Sprintf("http://%s:%d", cmd.InfluxHost, cmd.InfluxPort),
	})

	if err != nil {
		return err
	}

	// we only close the client once the function returns, as we don't return from this function unless error, but we will keep
	// using the influx client until an error happens
	defer influxClient.Close()

	ptConfig := influx.BatchPointsConfig{
		Database:  cmd.InfluxDBName,
		Precision: cmd.InfluxDBPrecision,
	}

	// start the HTTP server passing the influxClient in as a parameter for all
	http.HandleFunc("/edgex", timeInfluxData(influxClient, ptConfig))
	return http.ListenAndServe(fmt.Sprintf("%s:%d", cmd.HTTPHost, cmd.HTTPPort), nil)
}

func timeInfluxData(influxClient influx.Client, ptConfig influx.BatchPointsConfig) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		// time the function and output how long it took as well as the method
		start := time.Now()
		readingData(w, r, influxClient, ptConfig)
		log.Printf("%s %s %d us\n", r.URL.Path, r.Method, time.Since(start)/time.Microsecond)
	}
}

func readingData(w http.ResponseWriter, req *http.Request, influxClient influx.Client, ptConfig influx.BatchPointsConfig) {
	// make sure that the method is a post
	if strings.ToUpper(req.Method) != "POST" {
		// error only post is supported, add Allow header with just POST
		// and add MethodNotAllowed response code
		w.Header().Add("Allow", "POST")
		w.WriteHeader(http.StatusMethodNotAllowed)
		_, err := w.Write([]byte("invalid request, only POST supported"))
		if err != nil {
			log.Printf("error writing error response")
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
	go sendEventToInflux(influxClient, ptConfig, event)
	w.WriteHeader(http.StatusOK)
}

func sendEventToInflux(influxClient influx.Client, ptConfig influx.BatchPointsConfig, event models.Event) {
	// Make a new set of batch points for this event
	bp, _ := influx.NewBatchPoints(ptConfig)

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
		// note that the origin time is in milliseconds
		unixTime := float64(reading.Origin) / float64(time.Second/time.Millisecond)
		unixTimeSec := math.Floor(unixTime)
		unixTimeNSec := int64((unixTime - unixTimeSec) * float64(time.Second/time.Nanosecond))

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
			log.Printf("error creating reading point: %+v\n", err)
		}

		// Add it to the batch set
		bp.AddPoint(pt)
	}

	// finally write all these points out to influx
	err := influxClient.Write(bp)
	if err != nil {
		log.Printf("error writing points to influx: %+v\n", err)
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
var parser = flags.NewParser(&currentCmd, flags.Default)

// empty - the command execution happens in *.Execute methods
func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	_, err := parser.Parse()
	if err != nil {
		os.Exit(1)
	}
}
