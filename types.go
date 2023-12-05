// Package netchan provides an abstraction for network communication channels.
// It defines structures and functions to facilitate secure and efficient data
// exchange over network connections. This package is particularly focused on
// creating and managing channels that can send and receive structured data
// across network boundaries.
package netchan

// NetChanType represents the structure of data that is transmitted over
// the network channels managed by this package. It encapsulates the essential
// elements required for a secure and identifiable data transmission.
type NetChanType struct {
	// Id is a unique identifier for a message or a data payload. It is used
	// to track and reference the data throughout the transmission process.
	// The Id field should be unique for each message to ensure accurate
	// message tracking and handling.
	Id string

	// Secret is a security token or key associated with the data. This field
	// is used to authenticate the data and verify its integrity during
	// transmission. The Secret field plays a crucial role in ensuring that
	// the data is only accessible to intended and authorized parties.
	Secret string

	// Data represents the actual content or information being transmitted.
	// This field contains the payload of the message in a string format.
	// The Data field is flexible and can carry various types of information,
	// making this structure suitable for different data transmission needs.
	Data string
}
