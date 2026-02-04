## Web Crawler

A simple and efficient web crawler written in Go. This is designed for crawling web pages and following links to deepen exploration(BFS approch).

## Features

- Multi-threaded crawling for efficiency
- Bloom Filter for Duplicates URL
- Customizable depth and URL filtering
- Graceful handling of robots.txt
- Parsing HTML and extraction of links
- Added comments for easy work flow


# Run 
1. **Set Up Redis Stack with Docker**:
   - Pull the Redis Stack image:
     ```bash
     docker pull redis/redis-stack:latest
     ```
   - Run the Redis Stack container:
     ```bash
     docker run -d -p 6379:6379 --name redis-stack redis/redis-stack:latest
     ```
   - Verify the container is running:
     ```bash
     docker ps
     ```
