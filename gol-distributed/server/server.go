package main

import (
	"fmt"
	"net"
	"net/rpc"
	"sync"

	"uk.ac.bris.cs/gameoflife/gol"
	"uk.ac.bris.cs/gameoflife/util"
)

type WorkerTask struct {
	CurrentWorld [][]byte
	StartRow     int
	EndRow       int
	W            int
	H            int
	CollectFlips bool
}

type WorkerReply struct {
	NextWorld    [][]byte
	FlippedCells []util.Cell
	Delta        int
}

// GolWorker is the RPC server that processes Game of Life turns
type Broker struct { // Stateful, tracks current state of processing
	mu              sync.RWMutex // Mutex lock prevents race condition from ProcessTurns and GetAliveCellsCount
	currentWorld    [][]byte
	currentTurn     int
	totalTurns      int
	processing      bool
	aliveCellsCount int
	paused          bool
	shutdown        bool
	height          int
	width           int
	resumeCh        chan struct{} // Closed to resume when paused, recreated on pause, -> no busy waiting
	shutdownCh      chan struct{} // Closed to signal shutdown
	doneCh          chan struct{} // Closed when processing completes

	workerAddresses []string
}

// ProcessTurns evolves the Game of Life for the specified number of turns
func (b *Broker) ProcessTurns(req gol.Request, res *gol.Response) error {
	currentWorld := req.World
	nextWorld := worldCreation(req.Height, req.Width)

	workerAddresses := b.workerAddresses
	workerClients := make([]*rpc.Client, len(workerAddresses))

	for i, addr := range workerAddresses {
		client, err := rpc.Dial("tcp", addr)
		if err != nil {
			fmt.Println("Error dialing worker:", err)
			return nil
		} else {
			fmt.Println("Distributor connected to worker.")
		}
		workerClients[i] = client
		defer client.Close()
	}

	amountOfWorkers := len(workerClients)
	// Initialise
	b.mu.Lock()
	b.currentWorld = currentWorld
	b.currentTurn = 0
	b.totalTurns = req.Turns
	b.processing = true
	b.paused = false
	b.shutdown = false
	b.height = req.Height
	b.width = req.Width
	b.aliveCellsCount = countAliveCells(currentWorld)
	b.resumeCh = make(chan struct{})
	b.shutdownCh = make(chan struct{})
	b.doneCh = make(chan struct{})
	b.mu.Unlock()

	// Process all turns
	for turn := 0; turn < req.Turns; turn++ {
		rowsPerWorker := b.height / amountOfWorkers
		replies := make([]WorkerReply, amountOfWorkers)
		calls := make([]*rpc.Call, amountOfWorkers)
		b.mu.RLock()
		isPaused := b.paused
		resumeCh := b.resumeCh
		shutdownCh := b.shutdownCh
		isShutdown := b.shutdown
		b.mu.RUnlock()

		if isShutdown {
			break
		}
		if isPaused {
			select {
			case <-resumeCh: // wait here until Resume() closes resumeCh, then continue
			case <-shutdownCh:
				// Shutdown requested while paused
				b.mu.Lock()
				b.shutdown = true
				b.mu.Unlock()
				break
			}
			// Double-check shutdown after unblock, so we consider
			// a shutdown that happens right after resume, preventing another turn after shutdown req
			b.mu.RLock()
			if b.shutdown {
				b.mu.RUnlock()
				break
			}
			b.mu.RUnlock()
		}
		for i, client := range workerClients {
			var topBufferRow []byte
			startRow := i * rowsPerWorker

			if startRow == 0 {
				topBufferRow = b.currentWorld[b.height-1]
			} else {
				topBufferRow = b.currentWorld[startRow-1]
			}

			endRow := (i + 1) * rowsPerWorker
			var bottomBufferRow []byte

			if endRow >= b.height {
				endRow = b.height
			}
			if endRow == b.height {
				bottomBufferRow = b.currentWorld[0]
			} else {
				bottomBufferRow = b.currentWorld[endRow]
			}

			sliceToSend := getSliceFromWorld(currentWorld, startRow, endRow)
			bufferedSlice := bufferedSliceCreation(sliceToSend, topBufferRow, bottomBufferRow)

			workerTask := WorkerTask{
				CurrentWorld: bufferedSlice,
				StartRow:     0,
				EndRow:       len(bufferedSlice) - 1,
				W:            b.width,
				H:            len(bufferedSlice),
				CollectFlips: true,
			}
			fmt.Println(workerTask)
			calls[i] = client.Go(
				"GameWorker.CalculateNextState",
				workerTask,
				&replies[i],
				nil,
			)
		}

		// Computes the next world, and also tells us the difference in alive cells
		//turnFlippedCells := []util.Cell{}
		totalDelta := 0
		for i, call := range calls {
			<-call.Done
			if call.Error != nil {
				fmt.Printf("Error from worker %d: %s\n", i, call.Error)
				return nil
			}

			//turnFlippedCells = append(turnFlippedCells, replies[i].FlippedCells...)

			slice := replies[i].NextWorld // we receive an internal row not a buffered one

			startRow := i * rowsPerWorker
			for y := 0; y < len(slice); y++ {
				copy(nextWorld[startRow+y], slice[y])
			}
			totalDelta += replies[i].Delta
		}

		//allTurnsFlipped = append(allTurnsFlipped, turnFlippedCells)

		//delta, _ := calculateNextState(currentWorld, nextWorld, 0, req.Height, req.Height, req.Width, false)
		currentWorld, nextWorld = nextWorld, currentWorld

		// Update state (incremental alive cell count)
		b.mu.Lock()
		b.currentWorld = currentWorld
		b.currentTurn = turn + 1
		b.aliveCellsCount += totalDelta
		b.mu.Unlock()
	}

	// Main loop finishes

	// Collect co-ordinates of all alive cells in the world
	aliveCells := calculateAliveCells(currentWorld)

	// Update and signal processing to be complete
	b.mu.Lock()
	b.processing = false
	if b.doneCh != nil {
		close(b.doneCh)
	}
	b.mu.Unlock()

	// Fill RPC response
	res.World = currentWorld
	res.AliveCells = aliveCells
	return nil
}

// GetAliveCellsCount responds with the current number of alive cells and completed turns
func (b *Broker) GetAliveCellsCount(req gol.AliveCellsRequest, res *gol.AliveCellsResponse) error {
	b.mu.RLock()
	res.CellsCount = b.aliveCellsCount
	res.CompletedTurns = b.currentTurn
	b.mu.RUnlock()
	return nil
}

// Pause responds to the request by pausing processing, via updating the paused flag and creation of resume channel
func (b *Broker) Pause(req gol.PauseRequest, res *gol.PauseResponse) error {
	b.mu.Lock()
	if !b.paused {
		b.paused = true
		// Create a new resume channel to block waiters
		b.resumeCh = make(chan struct{})
	}
	res.Turn = b.currentTurn
	b.mu.Unlock()
	return nil
}

// Resume responds to the request by resuming processing, closing the resume channel to wake waiters, unblocking access
func (b *Broker) Resume(req gol.ResumeRequest, res *gol.ResumeResponse) error {
	b.mu.Lock()
	if b.paused {
		close(b.resumeCh)
		b.paused = false
	}
	b.mu.Unlock()
	return nil
}

// GetCurrentState responds with a snapshot of the current state - a copy of the world, alive cells and current turn
// StateRequest is when user asks to save or quit
func (b *Broker) GetCurrentState(req gol.StateRequest, res *gol.StateResponse) error {
	b.mu.RLock()
	defer b.mu.RUnlock()

	// Copy the current world, prevent data races
	worldCopy := make([][]byte, len(b.currentWorld))
	for i := range b.currentWorld {
		worldCopy[i] = make([]byte, len(b.currentWorld[i]))
		copy(worldCopy[i], b.currentWorld[i])
	}

	res.World = worldCopy
	res.AliveCells = calculateAliveCells(worldCopy)
	res.Turn = b.currentTurn
	return nil
}

// Shutdown signals the worker to shutdown and returns the final state
func (b *Broker) Shutdown(req gol.ShutdownRequest, res *gol.ShutdownResponse) error {
	b.mu.Lock()
	if !b.shutdown {
		b.shutdown = true
		if b.shutdownCh != nil {
			close(b.shutdownCh)
		}
	}
	doneCh := b.doneCh
	b.mu.Unlock()

	// Block until processing completes without busy wait
	if doneCh != nil {
		<-doneCh
	}

	b.mu.RLock()
	defer b.mu.RUnlock()

	// Copy the current world, prevent data races
	worldCopy := make([][]byte, len(b.currentWorld))
	for i := range b.currentWorld {
		worldCopy[i] = make([]byte, len(b.currentWorld[i]))
		copy(worldCopy[i], b.currentWorld[i])
	}

	res.World = worldCopy
	res.AliveCells = calculateAliveCells(worldCopy)
	res.Turn = b.currentTurn
	return nil
}

// countAliveCells returns the count of alive cells
func countAliveCells(world [][]byte) int {
	count := 0
	for _, row := range world {
		for _, cell := range row {
			if cell == 255 {
				count++
			}
		}
	}
	return count
}

// calculateAliveCells returns a slice of all alive cells
func calculateAliveCells(world [][]byte) []util.Cell {
	cells := []util.Cell{}

	for i := 0; i < len(world); i++ {
		for u := 0; u < len(world[i]); u++ {
			if world[i][u] == 255 {
				cells = append(cells, util.Cell{X: u, Y: i})
			}
		}
	}
	return cells
}

// startGolServer starts the RPC server for the GOL worker
func startGolServer(port string, b *Broker) error {
	// Register GolWorker type so the methods can be called via the RPC
	rpc.Register(b)
	// Listen for TCP connections on the port
	listener, err := net.Listen("tcp", ":"+port)
	if err != nil {
		return err
	}
	defer listener.Close()
	// Accept and handle RPC connections
	rpc.Accept(listener)
	return nil
}

// creates a new world using specified height and width
func worldCreation(height int, width int) [][]byte {
	world := make([][]byte, height)
	for i := range world {
		world[i] = make([]byte, width)
	}
	return world
}

// returns a slice from the world
func getSliceFromWorld(world [][]byte, top int, bottom int) [][]byte {
	newWorld := worldCreation(bottom-top, len(world[0]))
	for i := 0; i < bottom-top; i++ {
		newWorld[i] = world[top+i] // should be top + i instead of top + 1
	}
	return newWorld
}

// creates a new slice using the world and two buffer rows
func bufferedSliceCreation(world [][]byte, topBufferRow []byte, bottomBufferRow []byte) [][]byte {
	newWorld := worldCreation(len(world)+2, len(world[0]))
	for i := range newWorld {
		if i == 0 {
			newWorld[0] = topBufferRow
		} else if i == len(newWorld)-1 {
			newWorld[i] = bottomBufferRow
		} else {
			newWorld[i] = world[i-1]
		}
	}
	return newWorld
}
