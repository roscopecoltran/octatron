/*
Copyright (C) 2016 Andreas T Jonsson

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
GNU General Public License for more details.

You should have received a copy of the GNU General Public License
along with this program.  If not, see <http://www.gnu.org/licenses/>.
*/

package trace

import (
	"image"
	"image/color"
	"image/draw"
	"io"
	"math"
	"sync"

	"github.com/andreas-jonsson/octatron/pack"
	"github.com/ungerik/go3d/vec3"
)

type (
	Octree []octreeNode

	Camera struct {
		Position,
		LookAt,
		Up [3]float32
	}

	Config struct {
		FieldOfView  float32
		TreeScale    float32
		TreePosition [3]float32

		Tree   Octree
		Jitter bool
		Image  [2]draw.Image
	}

	Raytracer struct {
		cfg   Config
		frame uint32
		idx   int
		wg    sync.WaitGroup
	}
)

type (
	infiniteRay [2]vec3.T

	octreeNode struct {
		color    color.RGBA
		children [8]uint32
	}
)

func (n *octreeNode) setColor(color *pack.Color) {
	n.color.R = uint8(color.R * 255)
	n.color.G = uint8(color.G * 255)
	n.color.B = uint8(color.B * 255)
	n.color.A = uint8(color.A * 255)
}

func LoadOctree(reader io.Reader) (Octree, error) {
	var (
		color  pack.Color
		header pack.OctreeHeader
	)

	if err := pack.DecodeHeader(reader, &header); err != nil {
		return nil, err
	}

	data := make([]octreeNode, header.NumNodes)
	for i := range data {
		n := &data[i]
		if err := pack.DecodeNode(reader, header.Format, &color, n.children[:]); err != nil {
			return nil, err
		}
		n.setColor(&color)
	}

	return data, nil
}

func intersectBox(ray *infiniteRay, lenght float32, box *vec3.Box) float32 {
	origin := ray[0]
	direction := ray[1]

	oMin := vec3.Sub(&box.Min, &origin)
	oMax := vec3.Sub(&box.Max, &origin)

	oMin[0] /= direction[0]
	oMin[1] /= direction[1]
	oMin[2] /= direction[2]

	oMax[0] /= direction[0]
	oMax[1] /= direction[1]
	oMax[2] /= direction[2]

	mMax := vec3.Max(&oMax, &oMin)
	mMin := vec3.Min(&oMax, &oMin)

	final := math.Min(float64(mMax[0]), math.Min(float64(mMax[1]), float64(mMax[2])))
	start := math.Max(math.Max(float64(mMin[0]), 0.0), math.Max(float64(mMin[1]), float64(mMin[2])))

	dist := float32(math.Min(final, start))
	if final > start && dist < lenght {
		return dist
	}
	return lenght
}

var (
	clearColor color.RGBA

	childPositions = []vec3.T{
		vec3.T{0, 0, 0}, vec3.T{1, 0, 0}, vec3.T{0, 1, 0}, vec3.T{1, 1, 0},
		vec3.T{0, 0, 1}, vec3.T{1, 0, 1}, vec3.T{0, 1, 1}, vec3.T{1, 1, 1},
	}
)

func (rt *Raytracer) intersectTree(tree []octreeNode, ray *infiniteRay, nodePos vec3.T, nodeScale, length float32, nodeIndex uint32) (float32, color.RGBA) {
	var (
		color = clearColor
		node  = tree[nodeIndex]
	)

	box := vec3.Box{nodePos, vec3.T{nodePos[0] + nodeScale, nodePos[1] + nodeScale, nodePos[2] + nodeScale}}
	boxDist := intersectBox(ray, length, &box)

	if boxDist == length {
		return length, color
	}

	numChild := 0
	childScale := nodeScale * 0.5

	for i, childIndex := range node.children {
		if childIndex != 0 {
			numChild++
			scaled := childPositions[i].Scaled(childScale)
			pos := vec3.Add(&nodePos, &scaled)

			if ln, col := rt.intersectTree(tree, ray, pos, childScale, length, childIndex); ln < length {
				length = ln
				color = col
			}
		}
	}

	if numChild == 0 {
		return boxDist, node.color
	}

	return length, color
}

func (rt *Raytracer) calcIncVectors(camera *Camera, size image.Point) (vec3.T, vec3.T, vec3.T) {
	width := float32(size.X)
	height := float32(size.Y)

	lookAtPoint := vec3.T(camera.LookAt)
	eyePoint := vec3.T(camera.Position)
	up := vec3.T(camera.Up)

	viewDirection := vec3.Sub(&lookAtPoint, &eyePoint)
	u := vec3.Cross(&viewDirection, &up)
	v := vec3.Cross(&u, &viewDirection)
	u.Normalize()
	v.Normalize()

	viewPlaneHalfWidth := float32(math.Tan(float64(rt.cfg.FieldOfView / 2)))
	aspectRatio := height / width
	viewPlaneHalfHeight := aspectRatio * viewPlaneHalfWidth

	sV := v.Scaled(viewPlaneHalfHeight)
	sU := u.Scaled(viewPlaneHalfWidth)

	lookV := vec3.Sub(&lookAtPoint, &sV)
	viewPlaneBottomLeftPoint := vec3.Sub(&lookV, &sU)

	xIncVector := u.Scaled(2 * viewPlaneHalfWidth)
	yIncVector := v.Scaled(2 * viewPlaneHalfHeight)

	xIncVector[0] /= width
	xIncVector[1] /= width
	xIncVector[2] /= width

	yIncVector[0] /= height
	yIncVector[1] /= height
	yIncVector[2] /= height

	return xIncVector, yIncVector, viewPlaneBottomLeftPoint
}

func (rt *Raytracer) traceScanLine(h int, eyePoint, xInc, yInc, bottomLeft vec3.T) {
	cfg := &rt.cfg
	img := cfg.Image[rt.idx]
	size := img.Bounds().Max
	width, height := size.X, size.Y

	nodeScale := cfg.TreeScale
	nodePos := cfg.TreePosition
	tree := cfg.Tree

	step := 1
	if cfg.Jitter {
		step = 2
	}

	for w := rt.idx; w < width; w += step {
		x := xInc.Scaled(float32(w))
		y := yInc.Scaled(float32(h))

		x = vec3.Add(&x, &y)
		viewPlanePoint := vec3.Add(&bottomLeft, &x)

		dir := vec3.Sub(&viewPlanePoint, &eyePoint)
		dir.Normalize()

		ray := infiniteRay{eyePoint, dir}
		_, col := rt.intersectTree(tree, &ray, nodePos, nodeScale, math.MaxFloat32, 0)
		img.Set(w/step, (height-h)/step, col)
	}

	rt.wg.Done()
}

func (rt *Raytracer) Wait() int {
	rt.wg.Wait()
	return rt.idx
}

func (rt *Raytracer) Trace(camera *Camera) {
	rt.Wait()

	cfg := &rt.cfg
	rt.idx = int(rt.frame % 2)
	img := cfg.Image[rt.idx]
	size := img.Bounds().Max
	step := 1

	if cfg.Jitter {
		step = 2
		size.X *= 2
		size.Y *= 2
		rt.frame++
	}

	xInc, yInc, bottomLeft := rt.calcIncVectors(camera, size)

	height := size.Y
	rt.wg.Add(height / step)

	for y := rt.idx; y < height; y += step {
		go rt.traceScanLine(y, camera.Position, xInc, yInc, bottomLeft)
	}
}

func NewRaytracer(cfg Config) *Raytracer {
	return &Raytracer{cfg: cfg}
}
