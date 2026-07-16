export namespace desktop {
	
	export class SessionSummary {
	    id: string;
	    created_at: number;
	
	    static createFrom(source: any = {}) {
	        return new SessionSummary(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.created_at = source["created_at"];
	    }
	}
	export class UIConfig {
	    provider: string;
	    model: string;
	    mode: string;
	    cwd: string;
	    session_id: string;
	
	    static createFrom(source: any = {}) {
	        return new UIConfig(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.provider = source["provider"];
	        this.model = source["model"];
	        this.mode = source["mode"];
	        this.cwd = source["cwd"];
	        this.session_id = source["session_id"];
	    }
	}
	export class UIToolResult {
	    tool_call_id: string;
	    name: string;
	    output: string;
	    is_error: boolean;
	
	    static createFrom(source: any = {}) {
	        return new UIToolResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.tool_call_id = source["tool_call_id"];
	        this.name = source["name"];
	        this.output = source["output"];
	        this.is_error = source["is_error"];
	    }
	}
	export class UIToolCall {
	    id: string;
	    name: string;
	    arguments: Record<string, any>;
	    result?: UIToolResult;
	
	    static createFrom(source: any = {}) {
	        return new UIToolCall(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.arguments = source["arguments"];
	        this.result = this.convertValues(source["result"], UIToolResult);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class UIMessage {
	    id: string;
	    role: string;
	    text?: string;
	    thinking?: string;
	    tool_calls?: UIToolCall[];
	    tool_result?: UIToolResult;
	    timestamp?: number;
	
	    static createFrom(source: any = {}) {
	        return new UIMessage(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.role = source["role"];
	        this.text = source["text"];
	        this.thinking = source["thinking"];
	        this.tool_calls = this.convertValues(source["tool_calls"], UIToolCall);
	        this.tool_result = this.convertValues(source["tool_result"], UIToolResult);
	        this.timestamp = source["timestamp"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	

}

