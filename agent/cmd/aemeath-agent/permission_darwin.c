//go:build darwin && cgo

// NSBundle reads this section for command-line executables without an app bundle.
// Desktop applications use their own bundle Info.plist.
__attribute__((used, section("__TEXT,__info_plist")))
static const char aemeath_cli_info_plist[] =
    "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<!DOCTYPE plist PUBLIC \"-//Apple//DTD PLIST 1.0//EN\" \"http://www.apple.com/DTDs/PropertyList-1.0.dtd\">\n<plist version=\"1.0\"><dict><key>CFBundleIdentifier</key><string>dev.aemeath.agent.cli</string><key>CFBundleName</key><string>Aemeath CLI</string><key>NSMicrophoneUsageDescription</key><string>Aemeath uses the system microphone for standalone voice conversations and offline wake word detection.</string></dict></plist>\n";
