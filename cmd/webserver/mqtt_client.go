package main

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io/ioutil"

	MQTT "github.com/eclipse/paho.mqtt.golang"
)

func setupMQTTClient(cmd *StartCmd) (MQTT.Client, error) {
	// Make an options struct for the mqtt client
	connOpts := MQTT.NewClientOptions()

	// always use a clean session
	connOpts.SetCleanSession(true)

	// always reconnect
	connOpts.SetAutoReconnect(true)

	// set the connection and connection lost handler
	connOpts.SetConnectionLostHandler(onDisconnect)
	connOpts.SetOnConnectHandler(onConnect)

	// Setup the broker hostname, checking for SSL
	var brokerHost string
	if cmd.MQTTSSL {
		// need to include SSL in the URL
		brokerHost = fmt.Sprintf("ssl://%s:%d", cmd.MQTTHost, cmd.MQTTPort)

		conf := &tls.Config{
			InsecureSkipVerify: false,
		}

		// if an additional certificate authority is necessary,
		// load that ca certificate
		if cmd.MQTTSCertAuth != "" {
			cert, err := ioutil.ReadFile(cmd.MQTTSCertAuth)
			if err != nil {
				return nil, err
			}

			certPool := x509.NewCertPool()
			certPool.AppendCertsFromPEM(cert)

			conf.RootCAs = certPool
		}
		connOpts.SetTLSConfig(conf)
	} else {
		brokerHost = fmt.Sprintf("tcp://%s:%d", cmd.MQTTHost, cmd.MQTTPort)
	}
	connOpts.AddBroker(brokerHost)

	// Set the client ID
	var clientID string
	if cmd.MQTTClientName == "" {
		// Generate a new client name
		clientID = genNewClientID()
	} else {
		// Use what was specified in the options
		clientID = cmd.MQTTClientName
	}
	connOpts.SetClientID(clientID)

	// Setup username/password
	connOpts.SetUsername(cmd.MQTTUsername)
	connOpts.SetPassword(cmd.MQTTPassword)

	// Attempt to make the connection
	client := MQTT.NewClient(connOpts)
	if token := client.Connect(); token.Wait() && token.Error() != nil {
		return nil, token.Error()
	} else {
		// Now subscribe to the topic
		if token := client.Subscribe(cmd.MQTTTopic, byte(cmd.MQTTQoS), onMessageReceived); token.Wait() && token.Error() != nil {
			return nil, token.Error()
		}
		fmt.Printf("Connected to %s\n", brokerHost)
	}

	return client, nil
}
