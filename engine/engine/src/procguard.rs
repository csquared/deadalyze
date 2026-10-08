//! The engine's process discipline: its own process group, every child
//! registered so a cancel reaches them all, a watchdog that ends everything
//! when the parent is gone, and SIGTERM/SIGINT as the cancel signal the
//! orchestrator polls between stages.

use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::Mutex;
use std::time::Duration;

static CANCELLED: AtomicBool = AtomicBool::new(false);
static CHILDREN: Mutex<Vec<u32>> = Mutex::new(Vec::new());

extern "C" fn on_signal(_sig: libc::c_int) {
    CANCELLED.store(true, Ordering::SeqCst);
}

pub fn init() {
    unsafe {
        // Our own group, so a host can signal the engine and every leg at
        // once, and so the watchdog can end them all.
        libc::setpgid(0, 0);
        libc::signal(libc::SIGTERM, on_signal as libc::sighandler_t);
        libc::signal(libc::SIGINT, on_signal as libc::sighandler_t);
        // A host that closed its pipe is told so by SIGPIPE; we would rather
        // see the write fail and let the watchdog finish.
        libc::signal(libc::SIGPIPE, libc::SIG_IGN);
    }
    let parent = unsafe { libc::getppid() };
    std::thread::Builder::new()
        .name("watchdog".into())
        .spawn(move || loop {
            std::thread::sleep(Duration::from_millis(500));
            let now = unsafe { libc::getppid() };
            if now != parent || now == 1 {
                kill_children(libc::SIGKILL);
                unsafe { libc::_exit(130) };
            }
            if cancelled() {
                // Legs cannot be told to stop politely; the orchestrator
                // reports the cancel for every unfinished item.
                kill_children(libc::SIGKILL);
            }
        })
        .expect("spawn watchdog");
}

pub fn cancelled() -> bool {
    CANCELLED.load(Ordering::SeqCst)
}

pub fn register(pid: u32) {
    CHILDREN.lock().unwrap().push(pid);
}

pub fn unregister(pid: u32) {
    CHILDREN.lock().unwrap().retain(|p| *p != pid);
}

pub fn kill_children(sig: libc::c_int) {
    let pids: Vec<u32> = CHILDREN.lock().unwrap().clone();
    for pid in pids {
        unsafe {
            libc::kill(pid as libc::pid_t, sig);
        }
    }
}
