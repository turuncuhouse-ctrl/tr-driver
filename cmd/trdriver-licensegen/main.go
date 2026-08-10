package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"necipdrive/internal/license"
)

func main() {
	tier := flag.String("tier", "personal", "personal|small|medium|unlimited")
	years := flag.Int("years", 1, "validity in years (0 = no expiry)")
	customer := flag.String("customer", "", "customer label")
	note := flag.String("note", "", "optional note")
	flag.Parse()

	payload := license.Payload{
		Tier:     *tier,
		Customer: *customer,
		Note:     *note,
		Iat:      time.Now().Unix(),
	}
	if max, ok := license.MaxUsersForTier(*tier); ok {
		payload.MaxUsers = max
	} else {
		fmt.Fprintf(os.Stderr, "unknown tier %q\n", *tier)
		os.Exit(1)
	}
	if *years > 0 {
		payload.Exp = time.Now().AddDate(*years, 0, 0).Unix()
	}
	key, err := license.Sign(payload)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		fmt.Fprintln(os.Stderr, "Set LICENSE_PRIVATE_KEY (base64/hex seed) or LICENSE_ALLOW_DEV_SIGNING=1 for local test keys.")
		os.Exit(1)
	}
	fmt.Println(key)
}
