// Package sign signs container images and verifies their signatures in the
// cosign format, so orcinus, cosign and the cluster's admission policy
// (Kyverno) all read the same signatures:
//
//   - keys: ECDSA P-256; the private key is PKCS#8, encrypted with
//     scrypt + nacl/secretbox and PEM-wrapped as "ENCRYPTED SIGSTORE PRIVATE
//     KEY" (cosign's own format); the public key is a PKIX "PUBLIC KEY" PEM.
//   - signatures: a "simple signing" payload naming the image digest, signed
//     with the key and stored next to the image as the tag sha256-<hex>.sig.
//
// Signatures are not uploaded to a transparency log; verifiers must skip the
// tlog check (cosign --insecure-ignore-tlog, Kyverno ctlog.insecureIgnoreTlog).
package sign

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"

	"golang.org/x/crypto/nacl/secretbox"
	"golang.org/x/crypto/scrypt"
)

const (
	pemPrivate       = "ENCRYPTED SIGSTORE PRIVATE KEY"
	pemPrivateCosign = "ENCRYPTED COSIGN PRIVATE KEY" // older cosign releases
	pemPublic        = "PUBLIC KEY"
)

// encryptedKey is go-securesystemslib's "encrypted" envelope, which cosign uses.
type encryptedKey struct {
	KDF struct {
		Name   string `json:"name"`
		Params struct {
			N int `json:"N"`
			R int `json:"r"`
			P int `json:"p"`
		} `json:"params"`
		Salt []byte `json:"salt"`
	} `json:"kdf"`
	Cipher struct {
		Name  string `json:"name"`
		Nonce []byte `json:"nonce"`
	} `json:"cipher"`
	Ciphertext []byte `json:"ciphertext"`
}

// GenerateKey returns a new key pair as PEM: the private key encrypted with
// password (which may be empty), and the public key.
func GenerateKey(password []byte) (private, public []byte, err error) {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	der, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		return nil, nil, err
	}
	var e encryptedKey
	e.KDF.Name, e.Cipher.Name = "scrypt", "nacl/secretbox"
	e.KDF.Params.N, e.KDF.Params.R, e.KDF.Params.P = 32768, 8, 1
	e.KDF.Salt, e.Cipher.Nonce = make([]byte, 32), make([]byte, 24)
	if _, err := rand.Read(e.KDF.Salt); err != nil {
		return nil, nil, err
	}
	if _, err := rand.Read(e.Cipher.Nonce); err != nil {
		return nil, nil, err
	}
	box, err := e.boxKey(password)
	if err != nil {
		return nil, nil, err
	}
	var nonce [24]byte
	copy(nonce[:], e.Cipher.Nonce)
	e.Ciphertext = secretbox.Seal(nil, der, &nonce, box)
	body, err := json.Marshal(e)
	if err != nil {
		return nil, nil, err
	}
	pub, err := MarshalPublicKey(&k.PublicKey)
	if err != nil {
		return nil, nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: pemPrivate, Bytes: body}), pub, nil
}

func (e *encryptedKey) boxKey(password []byte) (*[32]byte, error) {
	if e.KDF.Name != "scrypt" || e.Cipher.Name != "nacl/secretbox" {
		return nil, fmt.Errorf("unsupported key encryption %s/%s", e.KDF.Name, e.Cipher.Name)
	}
	k, err := scrypt.Key(password, e.KDF.Salt, e.KDF.Params.N, e.KDF.Params.R, e.KDF.Params.P, 32)
	if err != nil {
		return nil, err
	}
	var box [32]byte
	copy(box[:], k)
	return &box, nil
}

// ParsePrivateKey decrypts a private key written by GenerateKey or by
// `cosign generate-key-pair`.
func ParsePrivateKey(data, password []byte) (*ecdsa.PrivateKey, error) {
	b, _ := pem.Decode(data)
	if b == nil || (b.Type != pemPrivate && b.Type != pemPrivateCosign) {
		return nil, errors.New("not an encrypted signing key (PEM " + pemPrivate + ")")
	}
	var e encryptedKey
	if err := json.Unmarshal(b.Bytes, &e); err != nil {
		return nil, fmt.Errorf("read signing key: %w", err)
	}
	box, err := e.boxKey(password)
	if err != nil {
		return nil, err
	}
	var nonce [24]byte
	copy(nonce[:], e.Cipher.Nonce)
	der, ok := secretbox.Open(nil, e.Ciphertext, &nonce, box)
	if !ok {
		return nil, errors.New("wrong password for the signing key")
	}
	k, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		return nil, err
	}
	ec, ok := k.(*ecdsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("signing key is %T; only ECDSA keys are supported", k)
	}
	return ec, nil
}

// MarshalPublicKey encodes a public key as PEM.
func MarshalPublicKey(k *ecdsa.PublicKey) ([]byte, error) {
	der, err := x509.MarshalPKIXPublicKey(k)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: pemPublic, Bytes: der}), nil
}

// ParsePublicKey reads a PEM public key (orcinus or cosign).
func ParsePublicKey(data []byte) (*ecdsa.PublicKey, error) {
	b, _ := pem.Decode(data)
	if b == nil || b.Type != pemPublic {
		return nil, errors.New("not a public key (PEM " + pemPublic + ")")
	}
	k, err := x509.ParsePKIXPublicKey(b.Bytes)
	if err != nil {
		return nil, err
	}
	ec, ok := k.(*ecdsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("public key is %T; only ECDSA keys are supported", k)
	}
	return ec, nil
}

// Fingerprint is a short, stable id for a public key (sha256 of its DER).
func Fingerprint(k *ecdsa.PublicKey) string {
	der, _ := x509.MarshalPKIXPublicKey(k)
	sum := sha256.Sum256(der)
	return "SHA256:" + hex.EncodeToString(sum[:])[:16]
}
