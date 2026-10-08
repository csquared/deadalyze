//! The event writer: one JSON object a line on stdout, `seq` counted per
//! batch, shared by every thread that has something to say.

use protocol::{Blob, Code, Event, Line, Task};
use std::io::{self, Write};
use std::sync::Mutex;

pub struct Out {
    inner: Mutex<(u64, Box<dyn Write + Send>)>,
}

impl Out {
    pub fn stdout() -> Out {
        Out {
            inner: Mutex::new((
                0,
                Box::new(io::BufWriter::with_capacity(1 << 20, io::stdout())),
            )),
        }
    }

    pub fn emit(&self, event: Event) {
        let mut g = self.inner.lock().unwrap();
        g.0 += 1;
        let line = Line { seq: g.0, event };
        // A write that fails means the host is gone; there is nobody to
        // tell, and the parent watchdog ends the process.
        let _ = serde_json::to_writer(&mut g.1, &line);
        let _ = g.1.write_all(b"\n");
        let _ = g.1.flush();
    }

    pub fn warning(&self, id: &str, task: Task, code: Code, message: impl Into<String>) {
        self.emit(Event::ItemWarning {
            id: id.into(),
            task,
            code,
            message: message.into(),
        });
    }

    pub fn error(
        &self,
        id: &str,
        task: Task,
        code: Code,
        message: impl Into<String>,
        retryable: bool,
    ) {
        self.emit(Event::ItemError {
            id: id.into(),
            task,
            code,
            message: message.into(),
            retryable,
        });
    }

    pub fn progress(&self, stage: &str, done: u32, total: u32) {
        self.emit(Event::Progress {
            stage: stage.into(),
            done,
            total,
            id: None,
        });
    }

    pub fn frame(&self, id: &str, f: &wave::Frame) {
        self.emit(Event::WaveformFrame {
            id: id.into(),
            offset: f.offset,
            columns: f.columns,
            columns_per_second: wave::COLUMNS_PER_SECOND,
            data: Blob::b64(&f.data),
        });
    }
}
