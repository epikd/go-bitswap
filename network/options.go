package network

import "github.com/libp2p/go-libp2p/core/protocol"

type NetOpt func(*Settings)

type Settings struct {
	ProtocolPrefix     protocol.ID
	SupportedProtocols []protocol.ID
	Filter             bool
	PSI                bool
}

func Prefix(prefix protocol.ID) NetOpt {
	return func(settings *Settings) {
		settings.ProtocolPrefix = prefix
	}
}

func SupportedProtocols(protos []protocol.ID) NetOpt {
	return func(settings *Settings) {
		settings.SupportedProtocols = protos
	}
}

func Filter(filter bool) NetOpt {
	return func(settings *Settings) {
		settings.Filter = filter
	}
}

func PSI(psi bool) NetOpt {
	return func(settings *Settings) {
		settings.PSI = psi
	}
}
