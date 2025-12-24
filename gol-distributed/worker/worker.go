package main

import (
	"fmt"

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

type GameWorker struct{}

func (w *GameWorker) CalculateNextState(task WorkerTask, reply *WorkerReply) (err error) {
	sliceHeight := task.EndRow - task.StartRow - 1 // we were off by 1
	nextWorldSlice := make([][]byte, sliceHeight)
	for i := range nextWorldSlice {
		nextWorldSlice[i] = make([]byte, task.W)
	}

	delta, flippedCells := calculateNextState(task.CurrentWorld, nextWorldSlice, task.H, task.W, task.CollectFlips)
	//fmt.Println(nextWorldSlice)
	//fmt.Println(flippedCells)
	reply.NextWorld = nextWorldSlice
	reply.FlippedCells = flippedCells
	reply.Delta = delta

	return
}

func calculateNextState(world [][]byte, nextWorld [][]byte, h int, w int, collectFlips bool) (int, []util.Cell) {
	startRow := 1
	endRow := len(world) - 2
	birthsMinusDeaths := 0
	// we were missimg the last internal row
	sliceSize := (endRow - startRow + 1) * w
	cells := make([]util.Cell, 0, sliceSize/4)
	for i := startRow; i <= endRow; i++ {
		for u := 0; u < w; u++ {
			// calculates number of adjacent nodes
			top := int(world[(i-1+h)%h][(u-1+w)%w]) + int(world[(i-1+h)%h][(u+w)%w]) + int(world[(i-1+h)%h][(u+1+w)%w])
			middle := int(world[(i+h)%h][(u-1+w)%w]) + int(world[(i+h)%h][(u+1+w)%w])
			bottom := int(world[(i+1+h)%h][(u-1+w)%w]) + int(world[(i+1+h)%h][(u+w)%w]) + int(world[(i+1+h)%h][(u+1+w)%w])
			sum := top + middle + bottom

			localRow := i - startRow
			// computes if cell is alive or dead
			if world[i][u] == 255 {
				if sum < 510 {
					nextWorld[localRow][u] = 0
					birthsMinusDeaths-- // death
					//fmt.Println(collectFlips)
					if collectFlips {
						cells = append(cells, util.Cell{X: u, Y: i})
					}
				} else if (sum == 510) || (sum == 765) {
					nextWorld[localRow][u] = 255
				} else {
					nextWorld[localRow][u] = 0
					birthsMinusDeaths-- // death
					if collectFlips {
						cells = append(cells, util.Cell{X: u, Y: i})
					}
				}
			} else {
				if sum == 765 {
					nextWorld[localRow][u] = 255
					birthsMinusDeaths++ // birth
					if collectFlips {
						cells = append(cells, util.Cell{X: u, Y: i})
					}
				} else {
					nextWorld[localRow][u] = 0
				}
			}
		}
	}
	//fmt.Println(cells)
	fmt.Println(birthsMinusDeaths)
	return birthsMinusDeaths, cells
}
