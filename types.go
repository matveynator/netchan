package netchan

type NetChanType struct {
	To            string //recepient network address and port hash (plain text)
	From          string //sender network address and port hash (encrypted by recepient public key)
	ChanName      string //channel name (encrypted by recepient public key)
	ChanData      string //channel data packed in GOB (encrypted by recepient public key)
	SessionSecret string //random per session secret (encrypted by recepient public key)
}
