package api

// ClientProtocolInfo is sent at pairing and on EVERY lease request. The server
// must gate dispatch on the current request, not on previously stored metadata.
type ClientProtocolInfo struct {
	InstanceID       string   `json:"instance_id,omitempty"`
	ProtocolVersions []int    `json:"protocol_versions,omitempty"`
	Capabilities     []string `json:"capabilities,omitempty"`
}

// CurrentProtocolInfo advertises ONLY methods fully implemented in the client.
// Do not report irc_search until its executor is implemented. Archive
// extraction is supported only through a server-issued per-job policy. Schema v3 is reserved for explicitly selected TLS policies.
func CurrentProtocolInfo(instanceID string) ClientProtocolInfo {
	return ClientProtocolInfo{
		InstanceID:       instanceID,
		ProtocolVersions: []int{1, 2, 3},
		Capabilities:     []string{"http_download", "xdcc_pack", "irc_command", "irc_tls_policy", "job_pause", "archive_extract"},
	}
}
