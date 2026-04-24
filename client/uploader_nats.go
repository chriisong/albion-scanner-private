package client

import (
	"fmt"

	"github.com/chriisong/albion-scanner-private/log"
	nats "github.com/nats-io/go-nats"
)

type natsUploader struct {
	isPrivate bool
	url       string
	nc        *nats.Conn
}

// newNATSUploader opens a NATS connection to url. Any connection error is
// returned so the caller can fail loudly at startup — the fork never falls
// back to public AODP, so a broker that is unreachable at boot must abort
// rather than silently drop messages.
func newNATSUploader(url string) (uploader, error) {
	nc, err := nats.Connect(url)
	if err != nil {
		return nil, fmt.Errorf("connect NATS broker %s: %w", url, err)
	}
	return &natsUploader{
		url: url,
		nc:  nc,
	}, nil
}

func (u *natsUploader) sendToIngest(body []byte, topic string, state *albionState, identifier string) {
	// not handling sending identifier since the official usage is with http_pow

	if err := u.nc.Publish(topic, body); err != nil {
		log.Errorf("Error while sending ingest to nats with data: %v", err)
	}
}
