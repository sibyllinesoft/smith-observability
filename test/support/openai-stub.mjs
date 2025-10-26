#!/usr/bin/env node
import http from 'node:http';
import { randomUUID } from 'node:crypto';

const port = Number.parseInt(process.env.OPENAI_STUB_PORT ?? '8080', 10);

const server = http.createServer((req, res) => {
  if (req.method === 'GET' && req.url === '/healthz') {
    res.writeHead(200, { 'content-type': 'application/json' });
    res.end(JSON.stringify({ status: 'ok' }));
    return;
  }

  const chunks = [];
  req.on('data', chunk => chunks.push(chunk));
  req.on('end', () => {
    const body = Buffer.concat(chunks).toString('utf8');
    console.log(
      JSON.stringify({
        level: 'info',
        msg: 'received request',
        method: req.method,
        url: req.url,
        body
      })
    );

    if (req.method === 'POST' && req.url === '/v1/responses') {
      try {
        const payload = JSON.parse(body || '{}');
        const toolCallId = `call_${randomUUID().replace(/-/g, '')}`;
        const responseId = `resp_${randomUUID().replace(/-/g, '')}`;
        const assistantMessageId = `msg_${randomUUID().replace(/-/g, '')}`;
        const functionCallMessageId = `callmsg_${randomUUID().replace(/-/g, '')}`;
        const toolResultMessageId = `tool_${randomUUID().replace(/-/g, '')}`;
        const finalAssistantMessageId = `msg_${randomUUID().replace(/-/g, '')}`;
        const createdAt = Math.floor(Date.now() / 1000);
        const finalResponse = {
          id: responseId,
          object: 'response',
          model: payload.model ?? 'gpt-4o-mini',
          created: createdAt,
          usage: {
            input_tokens: 128,
            output_tokens: 48,
            total_tokens: 176
          },
          status: 'completed',
          output: [
            {
              id: assistantMessageId,
              type: 'message',
              role: 'assistant',
              content: [
                {
                  type: 'output_text',
                  text: 'Observability stub agent acknowledging the prompt.'
                }
              ]
            },
            {
              id: functionCallMessageId,
              type: 'function_call',
              role: 'assistant',
              call_id: toolCallId,
              name: 'list_directory',
              arguments: JSON.stringify({ path: '.' })
            },
            {
              id: toolResultMessageId,
              type: 'function_call_output',
              role: 'tool',
              call_id: toolCallId,
              output: JSON.stringify(['README.md', 'package.json'])
            },
            {
              id: finalAssistantMessageId,
              type: 'message',
              role: 'assistant',
              content: [
                {
                  type: 'output_text',
                  text: 'Tool execution completed successfully.'
                }
              ]
            }
          ]
        };

        const writeEvent = data => {
          res.write(`data: ${JSON.stringify(data)}\n\n`);
        };

        res.writeHead(200, {
          'content-type': 'text/event-stream',
          'cache-control': 'no-cache',
          connection: 'keep-alive'
        });

        writeEvent({
          type: 'response.created',
          sequence_number: 0,
          response: {
            id: responseId,
            object: 'response',
            model: finalResponse.model,
            created_at: createdAt,
            status: 'in_progress'
          }
        });

        setTimeout(() => {
          writeEvent({
            type: 'response.output_text.delta',
            sequence_number: 1,
            response: { id: responseId },
            output_index: 0,
            content_index: 0,
            delta: 'Observability stub agent acknowledging the prompt.'
          });
        }, 10);

        setTimeout(() => {
          writeEvent({
            type: 'response.output_text.done',
            sequence_number: 2,
            response: { id: responseId },
            output_index: 0,
            content_index: 0,
            text: 'Observability stub agent acknowledging the prompt.'
          });
        }, 20);

        setTimeout(() => {
          writeEvent({
            type: 'response.completed',
            sequence_number: 3,
            response: {
              ...finalResponse,
              responses: finalResponse.output
            }
          });
          res.write('data: [DONE]\n\n');
          res.end();
        }, 30);
        return;
      } catch (error) {
        console.error(
          JSON.stringify({
            level: 'error',
            msg: 'failed to handle response payload',
            error: error.message
          })
        );
        res.writeHead(400, { 'content-type': 'application/json' });
        res.end(JSON.stringify({ error: 'invalid JSON' }));
        return;
      }
    }

    res.writeHead(404, { 'content-type': 'application/json' });
    res.end(JSON.stringify({ error: 'not found' }));
  });
});

server.listen(port, '0.0.0.0', () => {
  console.log(
    JSON.stringify({
      level: 'info',
      msg: 'openai stub listening',
      port
    })
  );
});
