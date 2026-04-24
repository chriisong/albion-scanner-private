package client

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/chriisong/albion-scanner-private/lib"
	"github.com/chriisong/albion-scanner-private/log"
)

type dispatcher struct {
	brokerUploader uploader
}

var (
	wsHub *WSHub
	dis   *dispatcher
)

// createDispatcher wires the package-level dispatcher with a single NATS broker
// uploader built from ConfigGlobal.PrivateBrokerURL. Returns an error rather
// than log.Fatal so the caller (Client.Run) can surface it through the normal
// exit path. The scanner has exactly one upload destination: the private broker.
func createDispatcher() error {
	dis = &dispatcher{}

	if !ConfigGlobal.DisableUpload {
		u, err := newBrokerUploader(ConfigGlobal.PrivateBrokerURL)
		if err != nil {
			return fmt.Errorf("configure private broker: %w", err)
		}
		dis.brokerUploader = u
	}

	if ConfigGlobal.EnableWebsockets {
		wsHub = newHub()
		go wsHub.run()
		go runHTTPServer()
	}
	return nil
}

// validateBrokerURL enforces that the URL is a non-empty nats:// endpoint.
// Split out from newBrokerUploader so scheme-validation tests don't need a
// reachable NATS server.
func validateBrokerURL(url string) error {
	if url == "" {
		return errors.New("PrivateBrokerURL is required; set -broker or ALBION_PRIVATE_BROKER_URL")
	}
	if !strings.HasPrefix(url, "nats://") {
		return fmt.Errorf("PrivateBrokerURL must use nats:// scheme (got %q); HTTP/PoW paths are removed in this fork", url)
	}
	return nil
}

// newBrokerUploader validates the URL and opens a NATS connection. Fails
// loudly when the URL is missing, uses a non-nats scheme, or the broker is
// unreachable at startup — the fork never falls back to public AODP.
func newBrokerUploader(url string) (uploader, error) {
	if err := validateBrokerURL(url); err != nil {
		return nil, err
	}
	return newNATSUploader(url)
}

func sendMsgToPublicUploaders(upload interface{}, topic string, state *albionState, identifier string) {
	if ConfigGlobal.DisableUpload {
		log.Info("Upload is disabled.")
		return
	}

	data, err := json.Marshal(upload)
	if err != nil {
		log.Errorf("Error while marshalling payload for %v: %v", err, topic)
		return
	}

	if dis != nil && dis.brokerUploader != nil {
		dis.brokerUploader.sendToIngest(data, topic, state, identifier)
	}

	if ConfigGlobal.EnableWebsockets {
		sendMsgToWebSockets(data, topic)
	}
}

func sendMsgToPrivateUploaders(upload lib.PersonalizedUpload, topic string, state *albionState, identifier string) {
	if ConfigGlobal.DisableUpload {
		log.Info("Upload is disabled.")
		return
	}

	// TODO: Re-enable this when issue #14 is fixed
	// Will personalize with blanks for now in order to allow people to see the format
	// if state.CharacterName == "" || state.CharacterId == "" {
	// 	log.Error("The player name or id has not been set. Please restart the game and make sure the client is running.")
	// 	notification.Push("The player name or id has not been set. Please restart the game and make sure the client is running.")
	// 	return
	// }

	upload.Personalize(state.CharacterId, state.CharacterName)

	data, err := json.Marshal(upload)
	if err != nil {
		log.Errorf("Error while marshalling payload for %v: %v", err, topic)
		return
	}

	if dis != nil && dis.brokerUploader != nil {
		dis.brokerUploader.sendToIngest(data, topic, state, identifier)
	}

	if ConfigGlobal.EnableWebsockets {
		sendMsgToWebSockets(data, topic)
	}
}

func runHTTPServer() {
	http.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		serveWs(wsHub, w, r)
	})

	err := http.ListenAndServe(":8099", nil)

	if err != nil {
		log.Panic("ListenAndServe: ", err)
	}
}

func sendMsgToWebSockets(msg []byte, topic string) {
	// TODO (gradius): send JSON data with topic string
	// TODO (gradius): this seems super hacky, and I'm sure there's a better way.
	var result string
	result = "{\"topic\": \"" + topic + "\", \"data\": " + string(msg) + "}"
	wsHub.broadcast <- []byte(result)
}
