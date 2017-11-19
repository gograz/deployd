package main

import (
	"crypto/hmac"
	"crypto/sha1"
	"fmt"
)

type signatureValidationError struct {
	Expected string
	Actual   string
}

func (e signatureValidationError) Error() string {
	return fmt.Sprintf("Signature validation failed. Expected: %s, actual: %s", e.Expected, e.Actual)
}

func verifySignature(payload *[]byte, signature, secret string) error {
	mac := hmac.New(sha1.New, []byte(secret))
	mac.Write(*payload)
	checkSum := mac.Sum(nil)
	expectedSignature := fmt.Sprintf("sha1=%x", checkSum)
	if expectedSignature != signature {
		return signatureValidationError{Expected: expectedSignature, Actual: signature}
	}
	return nil
}
