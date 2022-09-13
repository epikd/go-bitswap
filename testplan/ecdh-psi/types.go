package main

import (
	"encoding/json"
	"time"
)

func Marshal(v interface{}) string {
	q, _ := json.Marshal(v)
	return string(q)
}

type BitswapStat struct {
	*SingleDownloadSpeed
	*MultipleDownloadSpeed
}

type SingleDownloadSpeed struct {
	Cid              string        `json:"cid"`
	DownloadDuration time.Duration `json:"download_duration"`
}

type MultipleDownloadSpeed struct {
	BlockCount    int           `json:"block_count"` // number of blocks downloaded
	TotalDuration time.Duration `json:"total_duration"`
}
