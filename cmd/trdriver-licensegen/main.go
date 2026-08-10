package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"necipdrive/internal/license"
)

func main() {
	tier := flag.String("tier", "", "personal|small|medium|unlimited (optional if -request includes tier)")
	years := flag.Int("years", 1, "validity in years (0 = no expiry)")
	customer := flag.String("customer", "", "customer label")
	note := flag.String("note", "", "optional note")
	requestCode := flag.String("request", "", "customer TRDR1... demand code (preferred)")
	flag.Parse()

	if *requestCode != "" {
		req, err := license.ParseRequest(*requestCode)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		key, err := license.SignFromRequest(req, *tier, *years, *customer, *note)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			fmt.Fprintln(os.Stderr, "Set LICENSE_PRIVATE_KEY or LICENSE_ALLOW_DEV_SIGNING=1")
			os.Exit(1)
		}
		fmt.Println(key)
		return
	}

	if *tier == "" {
		*tier = "personal"
	}
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
	fmt.Fprintln(os.Stderr, "warning: unbound license (no -request). Prefer customer demand codes.")
	key, err := license.Sign(payload)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(key)
}
