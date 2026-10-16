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
    disconnect(): void;
    sendCtrlAltDel(): void;
    sendCredentials(c: { password?: string; username?: string }): void;
    focus(): void;
  }
}
