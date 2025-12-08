package main

import "github.com/dvictor357/blaze"

func main() {
	app := blaze.New()
	app.Use(blaze.Logger(), blaze.Recovery())

	app.GET("/", func(c *blaze.Context) error {
		return c.String(200, "Hello from Blaze!")
	})

	app.Listen(":8080")
}
