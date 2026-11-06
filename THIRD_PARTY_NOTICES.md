# References and Dependencies

The control-plane design was informed by
[maiphucgiang/codebuddy2api](https://github.com/maiphucgiang/codebuddy2api),
v1.5.4, commit `58fc829d701150b8d323875cb1a7a3d5eab8d710`.
The Go backend and embedded JavaScript implementation adapt the design to this
project. The reference is distributed under the following license:

MIT License

Copyright (c) 2026 墨菊

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.

Direct runtime dependencies are `github.com/lfhy/flag` (MIT),
`github.com/pelletier/go-toml/v2` (MIT), `golang.org/x/sys` (BSD-3-Clause),
and `modernc.org/sqlite` (BSD-3-Clause; SQLite itself is public domain).
The development-only browser test runner, `playwright-core`, is Apache-2.0.
Exact dependency versions, including transitive packages, are pinned in
`go.mod`, `go.sum`, and `package-lock.json`. Their original license files remain
available with the corresponding source modules.
