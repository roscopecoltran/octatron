/*************************************************************************/
/* Octatron                                                              */
/* Copyright (C) 2015 Andreas T Jonsson <mail@andreasjonsson.se>         */
/*                                                                       */
/* This program is free software: you can redistribute it and/or modify  */
/* it under the terms of the GNU General Public License as published by  */
/* the Free Software Foundation, either version 3 of the License, or     */
/* (at your option) any later version.                                   */
/*                                                                       */
/* This program is distributed in the hope that it will be useful,       */
/* but WITHOUT ANY WARRANTY; without even the implied warranty of        */
/* MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the         */
/* GNU General Public License for more details.                          */
/*                                                                       */
/* You should have received a copy of the GNU General Public License     */
/* along with this program.  If not, see <http://www.gnu.org/licenses/>. */
/*************************************************************************/

package octatron

import (
	"io"
	"sync"
	"sync/atomic"
)

type BuildConfig struct {
	Writer        io.WriteSeeker
	Bounds        Box
	VoxelsPerAxis int
	Format        OctreeFormat
}

type workerPrivateData struct {
	err    error
	worker Worker
}

func processSample(data *workerPrivateData, sample *Sample) {

}

func processData(data *workerPrivateData, node *treeNode, sampleChan <-chan Sample) error {
	for {
		sample, more := <-sampleChan
		if more == false {
			err := data.err
			if err != nil {
				return err
			}
			return nil
		}

		processSample(data, &sample)
		node.numSamples++
	}
}

func collectData(workerData *workerPrivateData, node *treeNode, sampleChan chan<- Sample) {
	err := workerData.worker.Start(node.bounds, sampleChan)
	if err != nil {
		workerData.err = err
	}
	close(sampleChan)
}

func incVolume(volumeTraversed *uint64, voxelsPerAxis int) uint64 {
	vpa := uint64(voxelsPerAxis)
	volume := vpa * vpa * vpa
	return atomic.AddUint64(volumeTraversed, volume)
}

func BuildTree(workers []Worker, cfg *BuildConfig) error {
	var volumeTraversed uint64
	vpa := uint64(cfg.VoxelsPerAxis)
	totalVolume := vpa * vpa * vpa

	numWorkers := len(workers)
	workerData := make([]workerPrivateData, numWorkers)

	writeMutex := &sync.Mutex{}

	nodeMapShutdownChan, nodeMapInChan, nodeMapOutChan := startNodeCache(numWorkers)
	nodeMapInChan <- newRootNode(cfg.Bounds, cfg.VoxelsPerAxis)

	defer func() {
		nodeMapShutdownChan <- struct{}{}
	}()

	var wgWorkers sync.WaitGroup
	wgWorkers.Add(numWorkers)

	for idx, worker := range workers {
		data := &workerData[idx]
		data.worker = worker

		// Spawn worker
		go func() {
			defer wgWorkers.Done()

			// Process jobs
			for {
				node, more := <-nodeMapOutChan
				if more == false {
					return
				}

				sampleChan := make(chan Sample, 10)
				go collectData(data, node, sampleChan)
				if processData(data, node, sampleChan) != nil {
					incVolume(&volumeTraversed, node.voxelsPerAxis)
					return
				}

				// This is a leaf
				if node.numSamples == 0 {
					parent := node.parent
					if parent != nil {
						parent.children[node.childIndex] = nil
					}

					// Are we done with the octree
					if incVolume(&volumeTraversed, node.voxelsPerAxis) == totalVolume {
						nodeMapShutdownChan <- struct{}{}
					}
				} else {
					hasChildren, err := node.serialize(cfg.Writer, writeMutex, cfg.Format, nodeMapInChan)
					if err != nil {
						incVolume(&volumeTraversed, node.voxelsPerAxis)
						data.err = err
						return
					} else if (hasChildren == false) {
						if incVolume(&volumeTraversed, node.voxelsPerAxis) == totalVolume {
							nodeMapShutdownChan <- struct{}{}
						}
					}
				}
			}
		}()
	}

	wgWorkers.Wait()
	for _, data := range workerData {
		if data.err != nil {
			return data.err
		}
	}
	return nil
}
