package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

var rootCmd *cobra.Command

func main() {
	rootCmd = &cobra.Command{
		Use:   "chrome2spall [myprofile.json]",
		Short: "A not particularly efficient utility to convert Chrome's performance profiles into spall files.",
		Args:  cobra.MatchAll(cobra.MaximumNArgs(1), cobra.OnlyValidArgs),
		Run: func(cmd *cobra.Command, args []string) {
			if len(args) == 0 {
				convertFile(os.Stdin)
			} else {
				if f, err := os.Open(args[0]); err == nil {
					convertFile(f)
				} else {
					fmt.Fprintf(os.Stderr, "Could not open file: %v\n", err)
				}
			}
		},
	}

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func convertFile(r io.Reader) {
	type profileState struct {
		Pid   int
		Time  int64
		Nodes map[int]Node
		Stack []int
	}
	profiles := make(map[int]*profileState)

	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		rawline := scanner.Text()
		line := strings.Trim(rawline, "[],\n")

		var event Event
		err := json.Unmarshal([]byte(line), &event)
		if err != nil {
			fmt.Fprintln(os.Stderr, "Error reading event:", err)
			continue
		}

		if event.IsSpecialEvent(SpecialEventProfile) {
			var args ProfileArgs
			err := json.Unmarshal(event.Args, &args)
			if err != nil {
				fmt.Fprintln(os.Stderr, "Failed to read Profile event:", err)
				continue
			}

			profiles[event.Pid] = &profileState{
				Pid:   event.Pid,
				Time:  args.Data.StartTime,
				Nodes: make(map[int]Node),
			}
		} else if event.IsSpecialEvent(SpecialEventProfileChunk) {
			var args ProfileChunkArgs
			err := json.Unmarshal(event.Args, &args)
			if err != nil {
				fmt.Fprintln(os.Stderr, "Failed to read ProfileChunk event:", err)
				continue
			}

			profile, ok := profiles[event.Pid]
			if !ok {
				fmt.Fprintf(os.Stderr, "Got an event for pid %v, but we never saw a Profile event for that pid\n", event.Pid)
				continue
			}

			for _, node := range args.Data.CPUProfile.Nodes {
				profile.Nodes[node.ID] = node
			}

			for i := range args.Data.CPUProfile.Samples {
				topNodeID := args.Data.CPUProfile.Samples[i]
				topNode := profile.Nodes[topNodeID]
				timeDelta := args.Data.TimeDeltas[i]

				profile.Time += timeDelta

				currentTopID := 0
				if len(profile.Stack) > 0 {
					currentTopID = profile.Stack[len(profile.Stack)-1]
				}

				if currentTopID == topNodeID {
					// no change, keep on ticking
				} else if topNode.CallFrame.CodeType == "other" && topNode.CallFrame.FunctionName == "(garbage collector)" {
					// Garbage collections are special. Don't treat them as a
					// stack change; push them as new events unconditionally.
					// They'll be popped by the next legitimate event.
					beginEvent := Event{
						Category: "function",
						Name:     topNode.CallFrame.FunctionName,
						Type:     "B",
						Pid:      event.Pid,
						Tid:      event.Tid,
						Time:     profile.Time,
					}
					fmt.Printf("%s,\n", string(must1(json.Marshal(beginEvent))))
					profile.Stack = append(profile.Stack, topNodeID)
				} else {
					// Stack change! Starting at new top node, follow parents
					// until you find an ancestor already in the stack (or
					// exhaust the stack.) Pop the stack back to that ancestor,
					// emitting end events. Then push all new nodes to the
					// stack, emitting begin events.

					// This will track the topmost node we want to keep.
					ancestorIndex := -1

					// First see if the top node is _in_ the stack. This means
					// we are purely popping.
					for i, id := range profile.Stack {
						if id == topNodeID {
							ancestorIndex = i
						}
					}

					var nodesToBegin []int

					// If we didn't find an ancestor yet, that means this is a
					// new event. Starting from that new event, work back
					// through the chain of parents until we find something in
					// the stack.
					if ancestorIndex < 0 {
						newTopNode := profile.Nodes[topNodeID]
						currentNodeID := newTopNode.ID

					findancestor:
						for currentNodeID != 0 {
							for i := len(profile.Stack) - 1; i >= 0; i-- {
								stackNode := profile.Stack[i]
								if stackNode == currentNodeID {
									ancestorIndex = i
									break findancestor
								}
							}

							nodesToBegin = append(nodesToBegin, currentNodeID)
							currentNodeID = profile.Nodes[currentNodeID].Parent
						}
					}

					// Now, pop back to the ancestor...
					for i := len(profile.Stack) - 1; i > ancestorIndex; i-- {
						endEvent := Event{
							Category: "function",
							Type:     "E",
							Pid:      event.Pid,
							Tid:      event.Tid,
							Time:     profile.Time,
						}
						fmt.Printf("%s,\n", string(must1(json.Marshal(endEvent))))
						profile.Stack = profile.Stack[:i]
					}

					// And then push the new events.
					for i := len(nodesToBegin) - 1; i >= 0; i-- {
						nodeID := nodesToBegin[i]
						node := profile.Nodes[nodeID]
						cf := node.CallFrame
						name := cf.FunctionName
						if name == "" {
							name = fmt.Sprintf("(anonymous %d:%d:%d)", cf.ScriptID, cf.LineNumber, cf.ColumnNumber)
						}
						beginEvent := Event{
							Category: "function",
							Name:     name,
							Type:     "B",
							Pid:      event.Pid,
							Tid:      event.Tid,
							Time:     profile.Time,
						}
						fmt.Printf("%s,\n", string(must1(json.Marshal(beginEvent))))
						profile.Stack = append(profile.Stack, nodeID)
					}
				}
			}
		} else {
			// pass the line through unchanged
			fmt.Fprintln(os.Stdout, rawline)
		}
	}
	if err := scanner.Err(); err != nil {
		fmt.Fprintln(os.Stderr, "reading standard input:", err)
	}
}

type Event struct {
	Name     string          `json:"name"`
	Category string          `json:"cat"`
	Type     string          `json:"ph"`
	Time     int64           `json:"ts"`
	Pid      int             `json:"pid"`
	Tid      int             `json:"tid"`
	Args     json.RawMessage `json:"args"`
}

func (e *Event) Categories() []string {
	return strings.Split(e.Category, ",")
}

func (e *Event) HasCategory(cat string) bool {
	for _, ecat := range e.Categories() {
		if cat == ecat {
			return true
		}
	}
	return false
}

func (e *Event) IsSpecialEvent(se SpecialEvent) bool {
	return e.HasCategory(se.Cat) && e.Type == se.Type && e.Name == se.Name
}

type SpecialEvent struct {
	Cat, Type, Name string
}

var (
	SpecialEventTracingStartedInBrowser = SpecialEvent{"disabled-by-default-devtools.timeline", "I", "TracingStartedInBrowser"}
	SpecialEventProfile                 = SpecialEvent{"disabled-by-default-v8.cpu_profiler", "P", "Profile"}
	SpecialEventProfileChunk            = SpecialEvent{"disabled-by-default-v8.cpu_profiler", "P", "ProfileChunk"}
)

type ProfileArgs struct {
	Data ProfileArgsData `json:"data"`
}

type ProfileArgsData struct {
	StartTime int64 `json:"startTime"`
}

type ProfileChunkArgs struct {
	Data ProfileChunkArgsData
}

type ProfileChunkArgsData struct {
	CPUProfile CPUProfile `json:"cpuProfile"`
	// Lines      []int      `json:"lines"`
	TimeDeltas []int64 `json:"timeDeltas"`
}

type CPUProfile struct {
	Nodes   []Node `json:"nodes"`
	Samples []int  `json:"samples"`
}

type Node struct {
	CallFrame CallFrame `json:"callFrame"`
	ID        int       `json:"id"`
	Parent    int       `json:"parent"`
}

type CallFrame struct {
	CodeType     string `json:"codeType"`
	FunctionName string `json:"functionName"`
	LineNumber   int    `json:"lineNumber"`
	ColumnNumber int    `json:"columnNumber"`
	ScriptID     int    `json:"scriptId"`
	URL          string `json:"url"`
}

func must1[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}
