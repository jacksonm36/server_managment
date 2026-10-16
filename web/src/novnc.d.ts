// Minimal types for the parts of noVNC's RFB client the console uses.
declare module "@novnc/novnc" {
  export default class RFB extends EventTarget {
    constructor(
      target: HTMLElement,
      urlOrChannel: string | WebSocket,
      options?: { credentials?: { password?: string; username?: string }; wsProtocols?: string[]; shared?: boolean },
    );
    scaleViewport: boolean;
    viewOnly: boolean;
    resizeSession: boolean;
    background: string;
    focusOnClick: boolean;
    qualityLevel: number;
    compressionLevel: number;
    clipViewport: boolean;
    dragViewport: boolean;
    clipboardPasteFrom(text: string): void;
    sendKey(keysym: number, code: string | null, down?: boolean): void;
    disconnect(): void;
    sendCtrlAltDel(): void;
    sendCredentials(c: { password?: string; username?: string }): void;
    focus(): void;
  }
}
