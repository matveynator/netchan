package netchan

type Message struct {
	To            string //recepient network address and port hash (plain text)
	From          string //sender network address and port hash (encrypted by recepient public key)
	Payload       interface{} //channel data packed in GOB (encrypted by recepient public key)
	Secret        string //random per session secret (encrypted by recepient public key)
}
