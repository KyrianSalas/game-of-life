package gol

import (
	"fmt"
	"net/rpc"
	"os"
	"time"

	"uk.ac.bris.cs/gameoflife/util"
)

type distributorChannels struct {
	events     chan<- Event
	ioCommand  chan<- ioCommand
	ioIdle     <-chan bool
	ioFilename chan<- string
	ioOutput   chan<- uint8
	ioInput    <-chan uint8
}

// Request contains the world state and parameters for processing
type Request struct {
	World  [][]byte
	Height int
	Width  int
	Turns  int
}

// Response contains the evolved world state and alive cells
type Response struct {
	World      [][]byte
	AliveCells []util.Cell
}

// Request and response for alive cell count, receiving the current no of alive cells and turn
type AliveCellsRequest struct{}
type AliveCellsResponse struct {
	CompletedTurns int
	CellsCount     int
}

// Request and response for pausing, receiving the turn it is paused on
type PauseRequest struct{}
type PauseResponse struct {
	Turn int
}

// Request and response for resuming
type ResumeRequest struct{}
type ResumeResponse struct{}

// Request and response for shutting down, receiving the final game state
type ShutdownRequest struct{}
type ShutdownResponse struct {
	World      [][]byte
	AliveCells []util.Cell
	Turn       int
}

// Request and response for the current state, receiving the current world, alive cells and turn
type StateRequest struct{}
type StateResponse struct {
	World      [][]byte
	AliveCells []util.Cell
	Turn       int
}

// distributor divides the work between workers and interacts with other goroutines.
func distributor(p Params, c distributorChannels, keyPresses <-chan rune) {
	h := p.ImageHeight
	w := p.ImageWidth

	// Connect to the GOL server, local for now
	serverAddr := os.Getenv("GOL_SERVER")
	if serverAddr == "" {
		serverAddr = "127.0.0.1:8030"
	}

	client, err := rpc.Dial("tcp", serverAddr)
	if err != nil {
		panic("Failed to connect to GOL broker: " + err.Error())
	}
	defer client.Close()

	filename := fmt.Sprintf("%dx%d", p.ImageWidth, p.ImageHeight)

	// Read initial world state from IO
	c.ioCommand <- ioInput
	c.ioFilename <- filename
	world := make([][]byte, h)
	for i := range world {
		world[i] = make([]byte, w)
	}

	// Create game world, and sends CellFlipped event for every alive cell
	for a := 0; a < h; a++ {
		for b := 0; b < w; b++ {
			world[a][b] = <-c.ioInput
			if world[a][b] == 255 {
				c.events <- CellFlipped{0, util.Cell{X: b, Y: a}}
			}
		}
	}

	// Sends a state change event to signal the simulation is now executing
	turn := 0
	c.events <- StateChange{turn, Executing}

	// Create a ticker for live cells reporting
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	done := make(chan bool)
	processingDone := make(chan *Response)
	paused := false
	pausedTurn := 0
	var pendingResponse *Response

	// This go routine will make RPC calls to get alive cells count from server, when the ticker fires
	go func() {
		for {
			select {
			case <-ticker.C:
				aliveCellsReq := AliveCellsRequest{}
				aliveCellsRes := new(AliveCellsResponse)
				err := client.Call("Broker.GetAliveCellsCount", aliveCellsReq, aliveCellsRes)
				if err == nil {
					turn = aliveCellsRes.CompletedTurns
					c.events <- AliveCellsCount{
						CompletedTurns: aliveCellsRes.CompletedTurns,
						CellsCount:     aliveCellsRes.CellsCount,
					}
				}
			case <-done:
				return
			}
		}
	}()

	// Start processing in a goroutine
	go func() {
		request := Request{
			World:  world,
			Height: h,
			Width:  w,
			Turns:  p.Turns,
		}
		response := new(Response)

		err := client.Call("Broker.ProcessTurns", request, response)
		if err != nil {
			fmt.Println("RPC call failed:", err.Error())
		}
		processingDone <- response
	}()

	// Handle keypresses and wait for processing to complete
	var finalWorld [][]byte
	var aliveCells []util.Cell
	// Consolidates shutdown logic
	finalise := func(resp *Response) {
		close(done)
		turn = p.Turns
		finalWorld = resp.World
		aliveCells = resp.AliveCells

		c.events <- CellsFlipped{turn, aliveCells}
		c.events <- TurnComplete{turn}
		c.events <- FinalTurnComplete{turn, aliveCells}
		saveCurrentState(p, c, finalWorld, turn)
		c.events <- StateChange{turn, Quitting}
		close(c.events)
	}

	// Central loop managing user input and handling worker completion
	for {
		select {
		case key := <-keyPresses:
			switch key {
			case 's':
				// Save current state
				stateReq := StateRequest{}
				stateRes := new(StateResponse)
				err := client.Call("Broker.GetCurrentState", stateReq, stateRes)
				if err == nil {
					saveCurrentState(p, c, stateRes.World, stateRes.Turn)
				}

			case 'q':
				// Closes controller client program, without causing error on the Gol server
				stateReq := StateRequest{}
				stateRes := new(StateResponse)
				err := client.Call("Broker.GetCurrentState", stateReq, stateRes)
				if err == nil {
					saveCurrentState(p, c, stateRes.World, stateRes.Turn)
					c.events <- StateChange{stateRes.Turn, Quitting}
				} else {
					c.events <- StateChange{turn, Quitting}
				}
				close(done)
				close(c.events)
				return

			case 'k':
				// All components of the distributed system shut down cleanly, and outputsa PGM image of the latest state
				shutdownReq := ShutdownRequest{}
				shutdownRes := new(ShutdownResponse)
				err := client.Call("Broker.Shutdown", shutdownReq, shutdownRes)
				if err == nil {
					saveCurrentState(p, c, shutdownRes.World, shutdownRes.Turn)
					c.events <- FinalTurnComplete{shutdownRes.Turn, shutdownRes.AliveCells}
				}
				close(done)
				c.events <- StateChange{shutdownRes.Turn, Quitting}
				close(c.events)
				os.Exit(0)
				return

			case 'p':
				if !paused {
					// Pause processing
					pauseReq := PauseRequest{}
					pauseRes := new(PauseResponse)
					err := client.Call("Broker.Pause", pauseReq, pauseRes)
					if err == nil {
						pausedTurn = pauseRes.Turn
						turn = pausedTurn
						fmt.Printf("Paused at turn %d\n", pausedTurn)
						c.events <- StateChange{pausedTurn, Paused}
						paused = true
					}
				} else {
					// Resume processing
					resumeReq := ResumeRequest{}
					resumeRes := new(ResumeResponse)
					err := client.Call("Broker.Resume", resumeReq, resumeRes)
					if err == nil {
						fmt.Println("Continuing")
						c.events <- StateChange{pausedTurn, Executing}
						paused = false
						if pendingResponse != nil {
							// Finalise now that processing had completed while paused
							finalise(pendingResponse)
							return
						}
					}
				}
			}
		case response := <-processingDone:
			// Processing completed; if paused, wait until resume to finalise
			if paused {
				pendingResponse = response
				continue
			}
			finalise(response)
			return
		}
	}
}

// writes the provided world to PGM image and emits ImageOutputComplete
func saveCurrentState(p Params, c distributorChannels, world [][]byte, turn int) {
	filename := fmt.Sprintf("%dx%dx%d", p.ImageWidth, p.ImageHeight, turn)
	c.ioCommand <- ioOutput
	c.ioFilename <- filename
	for a := 0; a < p.ImageHeight; a++ {
		for b := 0; b < p.ImageWidth; b++ {
			c.ioOutput <- world[a][b]
		}
	}
	// Make sure that the Io has finished any output before exiting.
	c.ioCommand <- ioCheckIdle
	<-c.ioIdle
	c.events <- ImageOutputComplete{CompletedTurns: turn, Filename: filename}
}
