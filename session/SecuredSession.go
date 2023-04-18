package session

import (
	"bytes"
	"crypto/sha256"
	"errors"
)

type SecuredSession struct {
	Curve         Curve25519
	AEAD          AEAD
	PeerPublicKey []byte
}

func NewSecuredSession(privateKey []byte) (session SecuredSession, err error) {
	if len(privateKey) == 0 {
		session.Curve, err = NewCurve25519()
	} else {
		privateKeySHA256 := sha256.Sum256(privateKey)
		session.Curve = NewStaticCurve25519(privateKeySHA256)
	}
	if err != nil {
		return session, err
	}
	return
}

func (session *SecuredSession) getSignature() ([]byte, error) {
	// Generating Shared Secret based on: MyPrivateKey + PeerPublicKey
	sharedSecret, err := session.Curve.GenerateSharedSecret(session.PeerPublicKey)
	if err != nil {
		return nil, err
	}

	hasher := sha256.New()
	hasher.Write(sharedSecret)

	return hasher.Sum(nil), nil
}

func (session *SecuredSession) GetSignature() ([]byte, error) {
	b, err := session.getSignature()
	return b, err
}

func (session *SecuredSession) Verify(peerSignature []byte) error {
	signature, err := session.getSignature()
	if err != nil {
		return err
	}

	// If the Peer is not a Man-In-The-Middle then Peer's Shared Secret is the Same!
	// Hash our Shared Secret to Compare with Peer's Signature!
	if !bytes.Equal(signature, peerSignature) {
		err2 := errors.New("signature and peerSignature are not Equal")
		return err2
	}

	// Generating Shared Secret based on: MyPrivateKey + PeerPublicKey
	sharedSecret, err := session.Curve.GenerateSharedSecret(session.PeerPublicKey)
	if err != nil {
		return err
	}

	/*
	   var nonces [32]byte // Just random data
	   if _, err := rand.Read(nonces[:]); err != nil {
	   	return err
	   }

	   var info []byte // Should be some public data
	*/
	peerKey, myKey, peerIV, myIV, err := DeriveKeysFromSharedSecret(sharedSecret, nil, nil)
	if err != nil {
		return err
	}

	// OK! Session is started! We can communicate now with AES Ephemeral Key!
	session.AEAD, err = NewAEAD(peerKey, myKey, peerIV, myIV)

	return err
}

func (session *SecuredSession) PeerVerify(peerSignature []byte) error {
	signature, err := session.getSignature()
	if err != nil {
		return err
	}

	// If the Peer is not a Man-In-The-Middle then Peer's Shared Secret is the Same!
	// Hash our Shared Secret to Compare with Peer's Signature!
	if !bytes.Equal(signature, peerSignature) {
		err2 := errors.New("signature and peerSignature are not Equal")
		return err2
	}

	// Generating Shared Secret based on: MyPrivateKey + PeerPublicKey
	sharedSecret, err := session.Curve.GenerateSharedSecret(session.PeerPublicKey)
	if err != nil {
		return err
	}

	peerKey, myKey, peerIV, myIV, err := DeriveKeysFromSharedSecret(sharedSecret, nil, nil)
	if err != nil {
		return err
	}

	// OK! Session is started! We can communicate now with AES Ephemeral Key!
	session.AEAD, err = NewAEAD(myKey, peerKey, myIV, peerIV)

	return err
}
