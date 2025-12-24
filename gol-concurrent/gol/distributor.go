package gol

import (
	//"flag"
	"fmt"
	//"net/rpc"
	"time" //for the ticker

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

type task struct {
	currentWorld [][]byte
	nextWorld    [][]byte
	startRow     int
	endRow       int
	w            int
	h            int
}

var ReverseHandler = "SecretStringOperations.Reverse"

type Response struct {
	Message string
}

type Request struct {
	Message string
}

// distributor divides the work between workers and interacts with other goroutines.
func distributor(p Params, c distributorChannels, keyPresses <-chan rune) {
	h := p.ImageHeight
	w := p.ImageWidth

	//amountOfWorkers := p.Threads

	filename := fmt.Sprintf("%dx%d", p.ImageWidth, p.ImageHeight)

	c.ioCommand <- ioInput
	c.ioFilename <- filename
	world := make([][]byte, h)
	for i := range world {
		world[i] = make([]byte, w)
	}

	for a := 0; a < h; a++ {
		for b := 0; b < w; b++ {
			world[a][b] = <-c.ioInput
			if world[a][b] == 255 {
				c.events <- CellFlipped{0, util.Cell{X: b, Y: a}}

			}
		}
	}

	turn := 0
	c.events <- StateChange{turn, Executing}

	currentWorld := world
	nextWorld := make([][]byte, h)
	for i := range nextWorld {
		nextWorld[i] = make([]byte, w)
	}

	// ticker sends value every 2 seconds, will be used to trigger a count of alive cells
	ticker := time.NewTicker(2 * time.Second)
	// stops the ticker when distributor exists
	defer ticker.Stop()

	taskChan := make(chan task, p.Threads)
	doneChan := make(chan []util.Cell, p.Threads)

	for i := 0; i < p.Threads; i++ {
		go worker(taskChan, doneChan)
	}

	keys := keyPresses
	quitRequest := false
	for turn < p.Turns && !quitRequest {
		rowsPerWorker := h / p.Threads
		for i := 0; i < p.Threads; i++ {

			startRow := i * rowsPerWorker
			endRow := (i + 1) * rowsPerWorker

			if i == p.Threads-1 {
				endRow = h
			}
			wTask := task{
				currentWorld: currentWorld,
				nextWorld:    nextWorld,
				startRow:     startRow,
				endRow:       endRow,
				w:            w,
				h:            h,
			}
			taskChan <- wTask
		}
		allCells := []util.Cell{}

		remaining := p.Threads
		for remaining > 0 {
			select {
			case cells := <-doneChan: // worker completed
				allCells = append(allCells, cells...)
				//fmt.Println(allCells)
				remaining--
			case <-ticker.C: // ticker fired, sends an AliveCellsCount event
				c.events <- AliveCellsCount{CompletedTurns: turn, CellsCount: len(calculateAliveCells(currentWorld))}
			case key := <-keys:
				switch key {
				case 's':
					saveCurrentState(p, c, currentWorld, turn)
				case 'q':
					quitRequest = true
				case 'p':
					if pauseLogic(p, c, currentWorld, turn, ticker.C, keys) { // Returns true if q pressed
						quitRequest = true
					}
				}
			}
		}
		c.events <- CellsFlipped{turn, allCells}
		//fmt.Println(allCells)
		currentWorld, nextWorld = nextWorld, currentWorld

		c.events <- TurnComplete{turn}
		turn++
		c.events <- StateChange{turn, Executing}
	}
	cells := calculateAliveCells(currentWorld)
	c.events <- FinalTurnComplete{turn, cells} // should use turn instead of p.Turns

	// save final state
	saveCurrentState(p, c, currentWorld, turn)

	c.events <- StateChange{turn, Quitting}

	// Close the channel to stop the SDL goroutine gracefully. Removing may cause deadlock.
	close(c.events)
}

// pauseLogic handles anything that happens whilst paused
func pauseLogic(p Params, c distributorChannels, currentWorld [][]byte, turn int, ticker <-chan time.Time, keys <-chan rune) bool {
	c.events <- StateChange{turn, Paused}
	for {
		select {
		case <-ticker:
			c.events <- AliveCellsCount{CompletedTurns: turn, CellsCount: len(calculateAliveCells(currentWorld))}
		case k := <-keys:
			switch k {
			case 'p':
				c.events <- StateChange{turn, Executing}
				return false
			case 's':
				saveCurrentState(p, c, currentWorld, turn)
			case 'q':
				return true
			}
		}
	}
}

func calculateNextState(world [][]byte, nextWorld [][]byte, startRow int, endRow int, h int, w int) []util.Cell {
	cells := []util.Cell{}
	for i := startRow; i < endRow; i++ { //iterates through columns of matrix
		for u := 0; u < w; u++ { // iterates through rows of matrix

			// calculates number of adjacent nodes
			top := int(world[(i-1+h)%h][(u-1+w)%w]) + int(world[(i-1+h)%h][(u+w)%w]) + int(world[(i-1+h)%h][(u+1+w)%w])
			middle := int(world[(i+h)%h][(u-1+w)%w]) + int(world[(i+h)%h][(u+1+w)%w])
			bottom := int(world[(i+1+h)%h][(u-1+w)%w]) + int(world[(i+1+h)%h][(u+w)%w]) + int(world[(i+1+h)%h][(u+1+w)%w])
			sum := top + middle + bottom

			// computes if cell is alive or dead
			if world[i][u] == 255 {
				if sum < 510 {
					nextWorld[i][u] = 0
					cells = append(cells, util.Cell{X: u, Y: i})
				} else if (sum == 510) || (sum == 765) {
					nextWorld[i][u] = 255
				} else {
					nextWorld[i][u] = 0
					cells = append(cells, util.Cell{X: u, Y: i})
				}
			} else {
				if sum == 765 {
					nextWorld[i][u] = 255
					cells = append(cells, util.Cell{X: u, Y: i})
				} else {
					nextWorld[i][u] = 0
				}
			}

		}
	}

	return cells
}
func worker(tasks <-chan task, done chan<- []util.Cell) {
	for task := range tasks {
		cells := calculateNextState(task.currentWorld, task.nextWorld, task.startRow, task.endRow, task.h, task.w)
		done <- cells
	}

}
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

/*
func connectToServer() {
	server := flag.String("server", "13.217.222.74", "IP:port string to connect to as server")
	flag.Parse()
	fmt.Println("Server: ", *server)

	client, _ := rpc.Dial("tcp", *server)
	defer client.Close()

	request := Request{Message: "Hello"}
	response := new(Response)
	client.Call(ReverseHandler, request, response)
	fmt.Println("Responded: ", response.Message)
}
*/
