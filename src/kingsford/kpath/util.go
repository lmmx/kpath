package main

import "log"

func DIE_IF(b bool, msg string, args ...interface{}) {
    if b {
        log.Fatalf("Error: "+msg, args...)
    }
}

// DIE_ON_ERR() logs a fatal error to the standard logger if err != nil and
// exits the program. It also prints the given informative message.
func DIE_ON_ERR(err error, msg string, args ...interface{}) {
	if err != nil {
		log.Printf("Error: "+msg, args...)
		log.Fatalf("%v", err)
	}
}


