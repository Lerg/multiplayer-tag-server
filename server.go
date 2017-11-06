package main

import (
	"flag"
	"html/template"
	"log"
	"math"
	"math/rand"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stojg/vector"
)

// Command line argument - server address
var addr = flag.String("addr", ":80", "game server address")

// Player JSON
type playerMessage struct {
	Id              int     `json:"id"`
	X               float64 `json:"x"`
	Y               float64 `json:"y"`
	Angle           float64 `json:"angle"`
	VelocityX       float64 `json:"velocity_x"`
	VelocityY       float64 `json:"velocity_y"`
	Turn            int8    `json:"turn"`
	Thrust          bool    `json:"thrust"`
	IsTag           bool    `json:"is_tag"`
	IsNew           bool    `json:"is_new"`
	HasDisconnected bool    `json:"has_disconnected"`
}

// Internal player data
type player struct {
	Connection   *websocket.Conn // Websocket connection of the player
	Id           int             // Player id
	X            float64         // Position vector
	Y            float64
	Angle        float64 // Direction angle
	VelocityX    float64 // Velocity vector
	VelocityY    float64
	Turn         int8  // Current turnining value
	Thrust       bool  // Current thruster state
	IsTag        bool  // Is this player the tag
	LastTagTime  int64 // When the last time the player was a tag
	writeChannel chan playerMessage
}

func (p *player) toMessage(isNew bool) playerMessage {
	return playerMessage{
		Id: p.Id,
		X:  p.X, Y: p.Y,
		Angle:     p.Angle,
		VelocityX: p.VelocityX, VelocityY: p.VelocityY,
		Turn:   p.Turn,
		Thrust: p.Thrust,
		IsTag:  p.IsTag,
		IsNew:  isNew}
}

func (p *player) send(message playerMessage) {
	p.writeChannel <- message
}

func (p *player) processWriteChannel() {
	for {
		message := <-p.writeChannel
		if err := p.Connection.WriteJSON(message); err != nil {
			log.Println(err)
		}
	}
}

// All players slice
var players = make([]*player, 0)

// Websocket upgrader from HTTP with default options
var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

var nextId = 0

const (
	mapWidth  = 1280.0
	mapHeight = 720.0
)

// Serve game websocket
func gameHandler(w http.ResponseWriter, r *http.Request) {
	connection, err := upgrader.Upgrade(w, r, nil)
	log.Println("open")
	if err != nil {
		log.Print("upgrade:", err)
		return
	}
	defer connection.Close()

	currentPlayer := new(player)
	currentPlayer.Connection = connection
	currentPlayer.Id = nextId
	currentPlayer.X = (rand.Float64() - 0.5) * mapWidth
	currentPlayer.Y = (rand.Float64() - 0.5) * mapHeight
	if len(players) == 0 {
		currentPlayer.IsTag = true // The first connected player is the tag
	}
	currentPlayer.LastTagTime = time.Now().Add(-3 * time.Second).UnixNano()
	currentPlayer.writeChannel = make(chan playerMessage, 100)

	nextId = nextId + 1
	players = append(players, currentPlayer)

	go currentPlayer.processWriteChannel()

	// Initialize the new player on the client
	currentPlayer.send(currentPlayer.toMessage(true))

	// Notify all other players about the new player
	go func() {
		for _, otherPlayer := range players {
			if otherPlayer.Id != currentPlayer.Id {
				currentPlayer.send(otherPlayer.toMessage(false))
				otherPlayer.send(currentPlayer.toMessage(false))
			}
		}
	}()

	for {
		message := playerMessage{}

		// Current player has made a move
		if err = currentPlayer.Connection.ReadJSON(&message); err != nil {
			log.Println("Player Disconnected waiting", err)
			var newTagPlayer *player
			for i, otherPlayer := range players {
				if otherPlayer.Id == currentPlayer.Id {
					players = append(players[:i], players[i+1:]...) // Remove player
					// If the player was the tag, choose another tag player randomly
					if currentPlayer.IsTag && len(players) > 0 {
						newTagPlayer = players[rand.Intn(len(players)-1)]
						newTagPlayer.IsTag = true
					}
					break
				}
			}
			// Notify other players
			for _, otherPlayer := range players {
				if newTagPlayer != nil {
					otherPlayer.send(newTagPlayer.toMessage(false))
				}
				otherPlayer.send(playerMessage{Id: currentPlayer.Id, HasDisconnected: true})
			}
			return
		}

		// Update internal player data
		currentPlayer.Angle = message.Angle
		currentPlayer.X = message.X
		currentPlayer.Y = message.Y
		currentPlayer.VelocityX = message.VelocityX
		currentPlayer.VelocityY = message.VelocityY
		currentPlayer.Turn = message.Turn
		currentPlayer.Thrust = message.Thrust

		// Good place to implement validation

		// Notify other players about current player update
		go func() {
			for _, otherPlayer := range players {
				if otherPlayer.Id != currentPlayer.Id {
					otherPlayer.send(currentPlayer.toMessage(false))
				}
			}
		}()
	}
}

const (
	turnSpeed = 0.1
	maxSpeed  = 5.0
	radius    = 50.0
	cooldown  = 3 * 1000 * 1000 * 1000 // Nanoseconds
)

func updateWorld() {
	// Tick according to 60 FPS
	for _ = range time.Tick(time.Second / 60.0) {
		// Perform movement calculations
		for _, p := range players {
			p.Angle = p.Angle - float64(p.Turn)*turnSpeed
			acceleration := 0.1
			if !p.Thrust {
				acceleration = 0
			}
			direction := vector.NewVector3(math.Cos(p.Angle), math.Sin(p.Angle), 0)
			velocity := vector.NewVector3(p.VelocityX, p.VelocityY, 0).Add(direction.Scale(acceleration))
			speed := velocity.Length()
			if speed > maxSpeed {
				velocity = velocity.Normalize().Scale(maxSpeed)
			} else if !p.Thrust && speed > 0 {
				velocity = velocity.Normalize().Scale(speed * 0.99)
			}
			p.VelocityX = velocity[0]
			p.VelocityY = velocity[1]

			p.X = p.X + velocity[0]
			p.Y = p.Y + velocity[1]
			xLimit, yLimit := 700.0, 520.0
			if p.X > xLimit {
				p.X = -xLimit
			} else if p.X < -xLimit {
				p.X = xLimit
			}
			if p.Y > yLimit+120 {
				p.Y = -yLimit
			} else if p.Y < -yLimit {
				p.Y = yLimit + 120
			}
		}

		// Check collisions
		var oldTagPlayer, newTagPlayer *player
		for i := 0; i < len(players)-1; i++ {
			for j := i + 1; j < len(players); j++ {
				firstPlayer := players[i]
				secondPlayer := players[j]
				firstPosition := vector.NewVector3(firstPlayer.X, firstPlayer.Y, 0)
				secondPosition := vector.NewVector3(secondPlayer.X, secondPlayer.Y, 0)
				distance := firstPosition.Sub(secondPosition).Length()
				if distance < radius {
					now := time.Now().UnixNano()
					if firstPlayer.IsTag {
						if now-secondPlayer.LastTagTime > cooldown {
							firstPlayer.IsTag = false
							firstPlayer.LastTagTime = now
							secondPlayer.IsTag = true
							oldTagPlayer = firstPlayer
							newTagPlayer = secondPlayer
						}
					} else if secondPlayer.IsTag {
						if now-firstPlayer.LastTagTime > cooldown {
							firstPlayer.IsTag = true
							secondPlayer.IsTag = false
							secondPlayer.LastTagTime = now
							oldTagPlayer = secondPlayer
							newTagPlayer = firstPlayer
						}
					}
				}
			}
		}
		// Notify other players about tag change
		if oldTagPlayer != nil && newTagPlayer != nil {
			go func() {
				for _, p := range players {
					p.send(oldTagPlayer.toMessage(false))
					p.send(newTagPlayer.toMessage(false))
				}
			}()
		}
	}
}

// Root homepage template
var statusTemplate = template.Must(template.New("").Parse(`
	<!DOCTYPE html>
	<html>
		<head>
			<meta charset="utf-8">
		</head>
		<body>
			Number of players online: {{.}}.
		</body>
	</html>
`))

// Serve status page
func statusHandler(w http.ResponseWriter, r *http.Request) {
	statusTemplate.Execute(w, len(players))
}

// Server start
func main() {
	flag.Parse()
	log.SetFlags(0)
	http.HandleFunc("/status", statusHandler) // Status page
	http.HandleFunc("/", gameHandler)         // Game websocket
	go updateWorld()
	log.Fatal(http.ListenAndServe(*addr, nil))
}
