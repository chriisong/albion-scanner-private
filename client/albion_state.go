package client

import (
	"strings"
	"regexp"
	"time"

	"github.com/chriisong/albion-scanner-private/lib"
	"github.com/chriisong/albion-scanner-private/log"
	"github.com/chriisong/albion-scanner-private/notification"
)

// CacheSize limit size of messages in cache
const CacheSize = 8192

type marketHistoryInfo struct {
	albionId  int32
	timescale lib.Timescale
	quality   uint8
}

type albionState struct {
	LocationId           string
	LocationString       string
	CharacterId          lib.CharacterID
	CharacterName        string
	GameServerIP         string
	AODataServerID       int
	AODataIngestBaseURL  string
	WaitingForMarketData bool
	BanditEventLastTimeSubmitted time.Time

	// A lot of information is sent out but not contained in the response when requesting marketHistory (e.g. ID)
	// This information is stored in marketHistoryInfo
	// This array acts as a type of cache for that info
	// The index is the message number (param255) % CacheSize
	marketHistoryIDLookup [CacheSize]marketHistoryInfo
	// TODO could this be improved?!
}

func (state albionState) IsValidLocation() bool {
	var onlydigits = regexp.MustCompile(`^[0-9]+$`)

	switch {
	case state.LocationId == "":
		log.Error("The players location has not yet been set. Please transition zones so the location can be identified.")
		if !ConfigGlobal.Debug {
			notification.Push("The players location has not yet been set. Please transition zones so the location can be identified.")
		}
		return false

	case onlydigits.MatchString(state.LocationId):
		return true
	case strings.HasPrefix(state.LocationId, "BLACKBANK-"):
		return true
	case strings.HasSuffix(state.LocationId, "-HellDen"):
		return true
	case strings.HasSuffix(state.LocationId, "-Auction2"):
		return true
	default:
		log.Error("The players location is not valid. Please transition zones so the location can be fixed.")
		if !ConfigGlobal.Debug {
			notification.Push("The players location is not valid. Please transition zones so the location can be fixed.")
		}
		return false
	}
}

// GetServer identifies the regional game server from the source IP and
// returns its integer server ID (1=west, 2=east, 3=europe, 0=unknown).
// The second return value (legacy AODataIngestBaseURL) is preserved for
// call-site compatibility but is always empty in this fork — the private
// scanner emits everything to ConfigGlobal.PrivateBrokerURL regardless of
// region, so the per-region AODP ingest URLs were stripped.
func (state albionState) GetServer() (int, string) {
	var serverID = 0

	// if we happen to have a server id stored in state, lets re-default to that
	if state.AODataServerID != 0 {
		serverID = state.AODataServerID
	}

	var isAlbionIP = false
	if strings.HasPrefix(state.GameServerIP, "5.188.125.") {
		serverID = 1
		isAlbionIP = true
	} else if strings.HasPrefix(state.GameServerIP, "5.45.187.") {
		serverID = 2
		isAlbionIP = true
	} else if strings.HasPrefix(state.GameServerIP, "193.169.238.") {
		serverID = 3
		isAlbionIP = true
	}

	if isAlbionIP {
		log.Tracef("Returning Server ID %v (ip src: %v)", serverID, state.GameServerIP)
	}

	return serverID, ""
}
