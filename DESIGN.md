IDEA 1:
what if i docouple here and push every text to a message queue and then consume it i.e push evry word/text into the NATS stream and then we would have have workers like 4 diffent workers, each woker having word range like w1-> char 'a' to 'g' and so on.
Then each worker would build Freq Map and then would buffer in memory and then push into DB, updating the DB freq, like information retrival.

IDEA 2: 
push freq map in a message queue and then each woker would have to consume the freq map from the queue based on sharding logic and then they would buffer and combine multiple message queue in memory and when each server reaches 60% memory or after 10sec each worker has to update the freq of each word present in buffer into the DB.

IDEA 3:
Instead of pushing one big freq map per URL... Push individual tuples to the right worker queue and rest idea 2 with WAL implementation idea (to be done later).